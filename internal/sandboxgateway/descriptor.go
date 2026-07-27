package sandboxgateway

// Defines the exact socket capability mounted into one sandbox.

// DescriptorConfig identifies one launch-owned sandbox socket generation.
type DescriptorConfig struct {
	HostSocket       string `json:"host_socket"`
	HostSocketDevice uint64 `json:"host_socket_device"`
	HostSocketInode  uint64 `json:"host_socket_inode"`
	SandboxSocket    string `json:"sandbox_socket"`
}
