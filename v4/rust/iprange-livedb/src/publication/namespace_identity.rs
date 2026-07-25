//! Stable reservation identity encoding.

use super::Identity;

impl Identity {
    pub(crate) fn encode(self) -> [u8; 32] {
        let mut bytes = [0; 32];
        bytes[..8].copy_from_slice(&self.device.to_le_bytes());
        bytes[8..16].copy_from_slice(&self.inode.to_le_bytes());
        bytes
    }

    pub(crate) fn decode(bytes: [u8; 32]) -> Option<Self> {
        if bytes == [0; 32] || bytes[16..].iter().any(|&byte| byte != 0) {
            return None;
        }
        Some(Self {
            device: u64::from_le_bytes(bytes[..8].try_into().expect("fixed identity device")),
            inode: u64::from_le_bytes(bytes[8..16].try_into().expect("fixed identity inode")),
        })
    }
}
