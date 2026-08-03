# Gitea Runner Cache and CI

The runner cache may hold pinned native VPP packages, runtime sources, Go caches, Debian packages, and firmware inputs. Cache contents are build inputs, never the source of product scope or credentials.

Suggested host layout:

```text
/opt/gitea-runner-ly-route/cache/
├── fdio-native/
├── runtime-src/
├── go/
├── apt/
└── firmware/
```

Only packages required by the product profile and approved VPP datapath tiers may enter an artifact. Native-path plugins are required; VPP DPDK packages may be included as the controlled fallback. CI must reject AF_XDP-copy helpers, generic Linux XDP forwarding, AF_PACKET, Linux forwarding fallback, or unapproved plugins.

The required target matrix is Gateway/Orchestrator × x86_64/ARM64. It must run repository verification, profile isolation, API contracts, native-first/DPDK-fallback selector tests, packet-path tests where capable, artifact inspection, and install/upgrade/rollback tests. Runner registration tokens and repository credentials belong in Gitea secret management and must not be copied into the repository, cache documentation, or command logs.

At the audited baseline the runner service is not registered, so remote CI execution remains an external-credential blocker. Local verification is still required before every handoff; restoring runner registration is tracked in the [Work Plan](work-plan.md).
