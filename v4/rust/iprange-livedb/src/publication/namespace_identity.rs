//! Stable reservation identity encoding.

use super::Identity;

const DEVICE_OFFSET: usize = 0;
const INODE_OFFSET: usize = 8;
const PAYLOAD_END: usize = 16;
const IDENTITY_SIZE: usize = 32;

impl Identity {
    pub(crate) fn encode(self) -> [u8; IDENTITY_SIZE] {
        let mut bytes = [0; IDENTITY_SIZE];
        bytes[DEVICE_OFFSET..INODE_OFFSET].copy_from_slice(&self.device.to_le_bytes());
        bytes[INODE_OFFSET..PAYLOAD_END].copy_from_slice(&self.inode.to_le_bytes());
        bytes
    }

    pub(crate) fn decode(bytes: [u8; IDENTITY_SIZE]) -> Option<Self> {
        if bytes == [0; IDENTITY_SIZE] || bytes[PAYLOAD_END..].iter().any(|&byte| byte != 0) {
            return None;
        }
        Some(Self {
            device: u64::from_le_bytes(
                bytes[DEVICE_OFFSET..INODE_OFFSET]
                    .try_into()
                    .expect("fixed identity device"),
            ),
            inode: u64::from_le_bytes(
                bytes[INODE_OFFSET..PAYLOAD_END]
                    .try_into()
                    .expect("fixed identity inode"),
            ),
        })
    }
}
