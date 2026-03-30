
microvm
- stdout and stderr are streaming as logs

- guest agent running inside microvm on startup using initrd
    - guest agent connecting to control plan via vsock unix socket 
    - control plane is able to stream stdout and stderr into herd logs
- microvm storage
    - using containerd to bootstrap dev mapper image to blocks and then using them to provision microvms
    - these microvm do copy on write to allow faster boot times.
- networking
    - [x] outbound working from inside the container [isolate local ips and ban them prob through host tunnels]
    - [x] host networking implemented using Point-to-Point `/32` IP allocation for strict VM isolation
    - [x] implemented `herd exec` command allowing interactive PTY into the container on runtime for debugging
    - [x] isolated MicroVM inbound/outbound networking with RFC 1918 DROP rules
- binary
    - need to make the binaries ephermal in the sense that initrd will just boot a shell for warm pool. or whatever the container image says. 
    - but what to run will need to come from api while acquiring the session, this will allow the user to essentially use any binary
    - need to make sure the binary will route stuff properly