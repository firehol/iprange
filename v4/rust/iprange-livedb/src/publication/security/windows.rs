//! Windows protected single-user DACL creation and proof.

use std::ffi::c_void;
use std::fs::File;
use std::io;
use std::mem::{size_of, zeroed};
use std::os::windows::ffi::OsStrExt;
use std::os::windows::io::{AsRawHandle, FromRawHandle};
use std::path::Path;
use std::ptr::{null, null_mut};

use sha2::{Digest, Sha256};
use windows_sys::Win32::Foundation::{
    CloseHandle, GetLastError, LocalFree, ERROR_INSUFFICIENT_BUFFER, ERROR_NO_TOKEN, HANDLE,
    INVALID_HANDLE_VALUE,
};
use windows_sys::Win32::Security::Authorization::{
    GetSecurityInfo, SetEntriesInAclW, EXPLICIT_ACCESS_W, SET_ACCESS, SE_FILE_OBJECT,
    TRUSTEE_IS_SID, TRUSTEE_IS_USER, TRUSTEE_W,
};
use windows_sys::Win32::Security::{
    AclSizeInformation, EqualSid, GetAce, GetAclInformation, GetLengthSid,
    GetSecurityDescriptorControl, GetTokenInformation, InitializeSecurityDescriptor, IsValidSid,
    SetSecurityDescriptorControl, SetSecurityDescriptorDacl, SetSecurityDescriptorOwner, TokenUser,
    ACL, ACL_SIZE_INFORMATION, DACL_SECURITY_INFORMATION, NO_INHERITANCE,
    OWNER_SECURITY_INFORMATION, PSID, SECURITY_ATTRIBUTES, SECURITY_DESCRIPTOR, SE_DACL_PROTECTED,
    TOKEN_QUERY, TOKEN_USER,
};
use windows_sys::Win32::Storage::FileSystem::{
    CreateFileW, CREATE_NEW, FILE_ALL_ACCESS, FILE_ATTRIBUTE_NORMAL, FILE_FLAG_OPEN_REPARSE_POINT,
    FILE_FLAG_WRITE_THROUGH, FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE,
};
use windows_sys::Win32::System::SystemServices::{
    ACCESS_ALLOWED_ACE_TYPE, SECURITY_DESCRIPTOR_REVISION,
};
use windows_sys::Win32::System::Threading::{
    GetCurrentProcess, GetCurrentThread, OpenProcessToken, OpenThreadToken,
};

use crate::publication::namespace::NamespaceError;

use super::COMMITMENT_DOMAIN;

#[derive(Clone, Debug)]
pub(crate) struct Profile {
    sid: Box<[u8]>,
    commitment: [u8; 32],
}

impl Profile {
    pub(crate) fn capture() -> Result<Self, NamespaceError> {
        let sid = effective_user_sid()?;
        let commitment = commitment(&sid);
        Ok(Self { sid, commitment })
    }

    pub(crate) fn commitment(&self) -> [u8; 32] {
        self.commitment
    }
}

pub(crate) fn create_private(
    path: &Path,
    profile: &Profile,
    write_through: bool,
) -> Result<File, NamespaceError> {
    let mut wide: Vec<u16> = path.as_os_str().encode_wide().collect();
    if wide.contains(&0) {
        return Err(NamespaceError::InvalidName);
    }
    wide.push(0);
    let descriptor = Descriptor::new(&profile.sid)?;
    let flags = FILE_ATTRIBUTE_NORMAL
        | FILE_FLAG_OPEN_REPARSE_POINT
        | if write_through {
            FILE_FLAG_WRITE_THROUGH
        } else {
            0
        };
    let handle = unsafe {
        CreateFileW(
            wide.as_ptr(),
            FILE_ALL_ACCESS,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            &descriptor.attributes,
            CREATE_NEW,
            flags,
            null_mut(),
        )
    };
    if handle == INVALID_HANDLE_VALUE {
        return Err(last_error("create private file"));
    }
    Ok(unsafe { File::from_raw_handle(handle) })
}

pub(crate) fn secure_creator_only(file: &File, profile: &Profile) -> Result<(), NamespaceError> {
    if creator_only_commitment(file)? == profile.commitment {
        Ok(())
    } else {
        Err(NamespaceError::AccessPolicy)
    }
}

pub(crate) fn creator_only_commitment(file: &File) -> Result<[u8; 32], NamespaceError> {
    SecurityInfo::read(file)?.verify()
}

struct Descriptor {
    descriptor: Box<SECURITY_DESCRIPTOR>,
    acl: *mut ACL,
    attributes: SECURITY_ATTRIBUTES,
}

impl Descriptor {
    fn new(sid: &[u8]) -> Result<Self, NamespaceError> {
        let trustee = TRUSTEE_W {
            pMultipleTrustee: null_mut(),
            MultipleTrusteeOperation: 0,
            TrusteeForm: TRUSTEE_IS_SID,
            TrusteeType: TRUSTEE_IS_USER,
            ptstrName: sid.as_ptr().cast_mut().cast(),
        };
        let access = EXPLICIT_ACCESS_W {
            grfAccessPermissions: FILE_ALL_ACCESS,
            grfAccessMode: SET_ACCESS,
            grfInheritance: NO_INHERITANCE,
            Trustee: trustee,
        };
        let mut acl = null_mut();
        let status = unsafe { SetEntriesInAclW(1, &access, null(), &mut acl) };
        if status != 0 {
            return Err(status_error("build creator-only DACL", status));
        }
        let mut descriptor = Box::new(unsafe { zeroed::<SECURITY_DESCRIPTOR>() });
        let owner = sid.as_ptr().cast_mut().cast();
        let raw_descriptor = descriptor.as_mut() as *mut SECURITY_DESCRIPTOR as *mut c_void;
        let initialized = unsafe {
            InitializeSecurityDescriptor(raw_descriptor, SECURITY_DESCRIPTOR_REVISION) != 0
                && SetSecurityDescriptorOwner(raw_descriptor, owner, 0) != 0
                && SetSecurityDescriptorDacl(raw_descriptor, 1, acl, 0) != 0
                && SetSecurityDescriptorControl(
                    raw_descriptor,
                    SE_DACL_PROTECTED,
                    SE_DACL_PROTECTED,
                ) != 0
        };
        if !initialized {
            unsafe {
                LocalFree(acl.cast());
            }
            return Err(last_error("build creator-only security descriptor"));
        }
        let attributes = SECURITY_ATTRIBUTES {
            nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
            lpSecurityDescriptor: descriptor.as_mut() as *mut SECURITY_DESCRIPTOR as *mut c_void,
            bInheritHandle: 0,
        };
        Ok(Self {
            descriptor,
            acl,
            attributes,
        })
    }
}

impl Drop for Descriptor {
    fn drop(&mut self) {
        self.attributes.lpSecurityDescriptor = null_mut();
        let _ = self.descriptor.as_ref();
        unsafe {
            LocalFree(self.acl.cast());
        }
    }
}

struct SecurityInfo {
    descriptor: *mut c_void,
    owner: PSID,
    dacl: *mut ACL,
}

impl SecurityInfo {
    fn read(file: &File) -> Result<Self, NamespaceError> {
        let mut owner = null_mut();
        let mut dacl = null_mut();
        let mut descriptor = null_mut();
        let status = unsafe {
            GetSecurityInfo(
                file.as_raw_handle(),
                SE_FILE_OBJECT,
                OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
                &mut owner,
                null_mut(),
                &mut dacl,
                null_mut(),
                &mut descriptor,
            )
        };
        if status != 0 {
            return Err(status_error("read creator-only DACL", status));
        }
        Ok(Self {
            descriptor,
            owner,
            dacl,
        })
    }

    fn verify(&self) -> Result<[u8; 32], NamespaceError> {
        if self.owner.is_null() || self.dacl.is_null() {
            return Err(NamespaceError::AccessPolicy);
        }
        let mut control = 0;
        let mut revision = 0;
        if unsafe { GetSecurityDescriptorControl(self.descriptor, &mut control, &mut revision) }
            == 0
            || control & SE_DACL_PROTECTED == 0
        {
            return Err(NamespaceError::AccessPolicy);
        }
        let mut size = ACL_SIZE_INFORMATION::default();
        if unsafe {
            GetAclInformation(
                self.dacl,
                (&mut size as *mut ACL_SIZE_INFORMATION).cast(),
                size_of::<ACL_SIZE_INFORMATION>() as u32,
                AclSizeInformation,
            )
        } == 0
            || size.AceCount != 1
        {
            return Err(NamespaceError::AccessPolicy);
        }
        let mut raw_ace = null_mut();
        if unsafe { GetAce(self.dacl, 0, &mut raw_ace) } == 0 {
            return Err(last_error("read creator-only DACL entry"));
        }
        let ace = unsafe { &*(raw_ace as *const windows_sys::Win32::Security::ACCESS_ALLOWED_ACE) };
        if u32::from(ace.Header.AceType) != ACCESS_ALLOWED_ACE_TYPE
            || ace.Header.AceFlags != 0
            || ace.Mask != FILE_ALL_ACCESS
        {
            return Err(NamespaceError::AccessPolicy);
        }
        let sid = (&ace.SidStart as *const u32).cast_mut().cast();
        if unsafe { EqualSid(self.owner, sid) } == 0 {
            return Err(NamespaceError::AccessPolicy);
        }
        Ok(commitment(&copy_sid(sid)?))
    }
}

impl Drop for SecurityInfo {
    fn drop(&mut self) {
        unsafe {
            LocalFree(self.descriptor);
        }
    }
}

fn effective_user_sid() -> Result<Box<[u8]>, NamespaceError> {
    let token = Token::effective()?;
    let mut length = 0;
    unsafe {
        GetTokenInformation(token.0, TokenUser, null_mut(), 0, &mut length);
    }
    if unsafe { GetLastError() } != ERROR_INSUFFICIENT_BUFFER
        || length < size_of::<TOKEN_USER>() as u32
    {
        return Err(last_error("size effective token user"));
    }
    let mut bytes = vec![0u8; length as usize];
    if unsafe {
        GetTokenInformation(
            token.0,
            TokenUser,
            bytes.as_mut_ptr().cast(),
            length,
            &mut length,
        )
    } == 0
    {
        return Err(last_error("read effective token user"));
    }
    let user = unsafe { std::ptr::read_unaligned(bytes.as_ptr().cast::<TOKEN_USER>()) };
    copy_sid(user.User.Sid)
}

fn copy_sid(sid: PSID) -> Result<Box<[u8]>, NamespaceError> {
    if sid.is_null() || unsafe { IsValidSid(sid) } == 0 {
        return Err(NamespaceError::AccessPolicy);
    }
    let length = unsafe { GetLengthSid(sid) } as usize;
    if length == 0 || length > 68 {
        return Err(NamespaceError::AccessPolicy);
    }
    let bytes = unsafe { std::slice::from_raw_parts(sid.cast::<u8>(), length) };
    Ok(bytes.to_vec().into_boxed_slice())
}

struct Token(HANDLE);

impl Token {
    fn effective() -> Result<Self, NamespaceError> {
        let mut token = null_mut();
        if unsafe { OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, 1, &mut token) } != 0 {
            return Ok(Self(token));
        }
        if unsafe { GetLastError() } != ERROR_NO_TOKEN {
            return Err(last_error("open effective thread token"));
        }
        if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
            return Err(last_error("open effective process token"));
        }
        Ok(Self(token))
    }
}

impl Drop for Token {
    fn drop(&mut self) {
        unsafe {
            CloseHandle(self.0);
        }
    }
}

fn commitment(sid: &[u8]) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(COMMITMENT_DOMAIN);
    hasher.update((sid.len() as u32).to_le_bytes());
    hasher.update(sid);
    hasher.update(FILE_ALL_ACCESS.to_le_bytes());
    hasher.update(SE_DACL_PROTECTED.to_le_bytes());
    hasher.finalize().into()
}

fn last_error(operation: &'static str) -> NamespaceError {
    NamespaceError::IoAt {
        operation,
        source: io::Error::last_os_error(),
    }
}

fn status_error(operation: &'static str, status: u32) -> NamespaceError {
    NamespaceError::IoAt {
        operation,
        source: io::Error::from_raw_os_error(status as i32),
    }
}
