# Herd Examples

This directory contains examples for using Herd in different scenarios, split by deployment mode.

## 📚 [Library Mode](./library)

Examples in this directory demonstrate how to use Herd as a **Go library** inside your own application. In this mode, your application manages the worker lifecycle and proxy routing directly in-process.

- **[ollama](./library/ollama)**: A one-process-per-agent Ollama gateway routing to stateful LLM workers.
- **[playwright](./library/playwright)**: A viewport/session-isolated Playwright server pool for browser automation.

---

## 🛰️ [Daemon Mode](./daemon)

Examples in this directory demonstrate how to interact with a standalone **Herd Daemon** (`herd start`). In this mode, the daemon manages workers, and external clients (in any language) communicate with it via the Control Plane (gRPC) and Data Plane (HTTP).

- **[python_client](./daemon/python_client)**: A Python client connecting and spawning workers via the gRPC control plane and making proxy calls.
