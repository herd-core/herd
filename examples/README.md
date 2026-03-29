# Herd Examples

This directory contains examples for using Herd in different scenarios, split by deployment mode.



## 🛰️ [Daemon Mode](./daemon)

Examples in this directory demonstrate how to interact with a standalone **Herd Daemon** (`herd start`). In this mode, the daemon manages workers, and external clients (in any language) communicate with it via the Control Plane (gRPC) and Data Plane (HTTP).

- **[python_client](./daemon/python_client)**: A Python client connecting and spawning workers via the gRPC control plane and making proxy calls.
