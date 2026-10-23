# Changelog

## 0.1.0 (2026-10-23)


### Features

* active probing of local endpoints ([20b0971](https://github.com/TobiasMende/hcloud-fip-announcer/commit/20b0971de852dfa99368c31e39894033f85d5dbf))
* **api:** FloatingIPGroup v1alpha1 ([d727d53](https://github.com/TobiasMende/hcloud-fip-announcer/commit/d727d5309cb2e95df1b158ae06c9c1b908cd52d4))
* bind Services of the class to group addresses ([67e8e18](https://github.com/TobiasMende/hcloud-fip-announcer/commit/67e8e18ed452487483a09484ba807fa3949a3c17))
* **chart:** control-plane placement docs, secure-metrics RBAC, single-replica PDB ([30411fd](https://github.com/TobiasMende/hcloud-fip-announcer/commit/30411fd642820aadfcc94d5d6af2fd2a056020f8))
* **chart:** Helm chart with dashboard and alerts ([dd0d1c6](https://github.com/TobiasMende/hcloud-fip-announcer/commit/dd0d1c64033440ce7c8fe60700f7f622dfb35aaf))
* group reconciler with placement and Hetzner lifecycle ([f13d648](https://github.com/TobiasMende/hcloud-fip-announcer/commit/f13d64857d21e962f4082dde0858d4a467ec97c3))
* hcloud-fip-announcer v0.1 ([f0e2dcf](https://github.com/TobiasMende/hcloud-fip-announcer/commit/f0e2dcfbd218d228ea1f92cf630c5ef5da1effab))
* **hetzner:** client wrapper with fake ([ddccc8d](https://github.com/TobiasMende/hcloud-fip-announcer/commit/ddccc8d3cfafd5bd16a94be205c837ae8bd7c123))
* **hetzner:** ServerByName, FIP network zone, fake error sequences ([cf24a9f](https://github.com/TobiasMende/hcloud-fip-announcer/commit/cf24a9f138f7e5e16494c30b518633a05bd2c640))
* node eligibility ([b76f4b9](https://github.com/TobiasMende/hcloud-fip-announcer/commit/b76f4b97228d5dbd2b9c4951733425ace00ea4aa))
* placement decision ([0a5902a](https://github.com/TobiasMende/hcloud-fip-announcer/commit/0a5902aff9a54d569d221bfb1d0fbf28e922b0c7))
* production logging and a readable leader election lease ([4fb66bb](https://github.com/TobiasMende/hcloud-fip-announcer/commit/4fb66bbb7b776bc34a821f800d42aa96e7ffbc20))


### Bug Fixes

* **chart:** CI helm v4, drift check, alert tuning ([8a65c3d](https://github.com/TobiasMende/hcloud-fip-announcer/commit/8a65c3d0c8c79e01a7990bd5119a1cce990dda07))
* **hetzner:** honor rate-limit headers, race-free fake ([582de43](https://github.com/TobiasMende/hcloud-fip-announcer/commit/582de431c3e98e03433619bed404ff90f7be4a7e))
* keep fresher FIP entries across refresh ([c43972b](https://github.com/TobiasMende/hcloud-fip-announcer/commit/c43972b9584ea5897daf21c7f2022a4aeb9e8059))
* look Floating IPs up by name before creating, keep them protected ([32f8c99](https://github.com/TobiasMende/hcloud-fip-announcer/commit/32f8c990dd8dd907dc087853b81373412ed431c0))
* **probe:** validate flags, no redirects ([960bbb5](https://github.com/TobiasMende/hcloud-fip-announcer/commit/960bbb564b6c26b23e46bcb364da09cbce06615e))
* release Service addresses when the group goes away ([51536b8](https://github.com/TobiasMende/hcloud-fip-announcer/commit/51536b87bc045728140f46feb9773a6fe44ef0ea))
* shared Hetzner list snapshot, concurrency and review findings ([bb73ee0](https://github.com/TobiasMende/hcloud-fip-announcer/commit/bb73ee0a8cdf9a2f8e276a4e42d58bde5e24a8a1))
* tighten service binding edge cases ([0ea6788](https://github.com/TobiasMende/hcloud-fip-announcer/commit/0ea67888503af8ee4f59fc662baaa982e9ad5dea))
* zeroed move counters, default check on invalid annotation, IPv6 validation ([4aa08d1](https://github.com/TobiasMende/hcloud-fip-announcer/commit/4aa08d1787df0e9877f0f08fa1bfbc9e8c6d93c7))


### Miscellaneous Chores

* release 0.1.0 ([6b6332c](https://github.com/TobiasMende/hcloud-fip-announcer/commit/6b6332c64ef73418dc087d7c0a4984f9fb90242e))
