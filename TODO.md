TODO

This is a bunch of suggested things I want to add to poprocks

1. vsock modules - premade messaging setups that provide common vsock functionalities, like:
    1. http - allows an http pass through - mitm if possible to inject credentials
    2. api - using the http, convert an api instantly to a module
    3. file transfer
    4. retry/debug wrapper for streaming sends so replayable sources can be retried (example: retain source file path and reopen buffered reader on retry)
2. Adding messengers together combines the messengers into a singular entity that share the connetion.
    - also additional easier ways to combine additional functionality to messengers?
3. Networking
    1. All of networking primitives
    2. Firewall settings
    3. If possible MitM style wrapping
4. Service registration - ie can a generic registry be created that clients can ask for a generic service and get access back? Or common services, like LLM providers or SERP providers, being available as generic services?
5. Orchestrator - a system for running multiple microvms as a unit
    - job queueing
6. Env variables / secrets management for VMs
7. Wrapper VM + build system for non Linux OSes
8. Overlay images for rollback of volumes / VMs
9. Snapshotting of VMs