# Herd: Microvm Hypervisor

The initial goal was to create a lightweight, secure, and fast execution environment for running docker images. The problem with docker is that it has a major security issue with running containers on the same host. Any zero day exploit in the kernel through a container can lead to a full host compromise. 

## Different from firecracker

Firecracker microvm is an amazing peice of technology but it's just a dumb hypervisor. It doesn't provide any host side setup for running OCI images, or networking, or ingress, or anything. You have to build all of that yourself. Herd provides all of that out of the box. 

The table below highlights the difference between firecracker and herd.

| Feature | Raw Firecracker | Herd |
| :--- | :--- | :--- |
| **Input** | Custom Kernel + Raw `ext4` Disk Image | Standard Docker/OCI Image |
| **Storage** | Manually create disk images using `dd` | **OCI Translation**: Automated image-to-snapshot. |
| **Network** | Creates a TAP device, you route the rest | **Automated IPAM**: Host side NAT + routing. |
| **Ingress** | No ingress | **Wake-on-Request Proxy**: Host port binding. |
|**Isolation** | Manually configure jailer for each microvm | **Automated Isolation**: Herd auto configures jailer for each microvm. |
| **Lifecycle** | Turn On / Turn Off | **Scale to Zero**: Cold-boots on first request [WIP]. |
| **User Experience/Complexity** | Systems Engineer (Hard) | Application Developer (Easy) |


## 🛠️ Installation & Running

### 1. Prerequisites

- **Host OS**: Linux (A recent kernel with KVM support).
- **Virtualization**: Hardware virtualization (VT-x or AMD-V) must be enabled in the BIOS/UEFI.
- **KVM Access**: The `/dev/kvm` device must exist and be accessible.
    ```bash
    ls -l /dev/kvm
    ```
- **Root Access**: Most `herd` commands require `sudo`.

Before installing Herd, ensure your system has `containerd` and `iptables` installed:

```bash
sudo apt update && sudo apt install -y containerd iptables
```

### 2. Quick Install

```bash
curl -sSL https://raw.githubusercontent.com/herd-core/herd/main/scripts/install.sh | bash
```

### 2. Initialize Host

```bash
# Prepare the host (loop devices, devmapper, containerd config)
sudo herd init

# Or in non-interactive mode:
sudo herd init --yes
```


### 3. Start the Daemon

```bash
sudo herd start
```

### 4. Deploy a MicroVM

```bash
herd deploy --image nginx:latest
```

*Note: Herd requires `sudo` for managing KVM, TAP devices, and devmapper snapshots.*

For more details, see [CLI & Configuration Reference](./docs/cli.md).
