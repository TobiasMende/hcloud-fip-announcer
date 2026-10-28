# Changelog

## [0.4.1](https://github.com/TobiasMende/hcloud-fip-announcer/compare/v0.4.0...v0.4.1) (2026-10-28)


### Bug Fixes

* **deps:** update kubernetes modules to v0.37.2 ([#37](https://github.com/TobiasMende/hcloud-fip-announcer/issues/37)) ([4c14992](https://github.com/TobiasMende/hcloud-fip-announcer/commit/4c149927406b59299b905980aa90943caafd5e8c))

## [0.4.0](https://github.com/TobiasMende/hcloud-fip-announcer/compare/v0.3.0...v0.4.0) (2026-10-24)


### Features

* clearer kubectl columns for FloatingIPGroup ([02e8c71](https://github.com/TobiasMende/hcloud-fip-announcer/commit/02e8c712e640a5b2ed988ec83fd7322abb3d39c3))
* clearer kubectl columns for FloatingIPGroup ([6cd5ee6](https://github.com/TobiasMende/hcloud-fip-announcer/commit/6cd5ee6fd90599243370ae2c674257ceed896562))

## [0.3.0](https://github.com/TobiasMende/hcloud-fip-announcer/compare/v0.2.0...v0.3.0) (2026-10-24)


### Features

* probe duration metric ([016f693](https://github.com/TobiasMende/hcloud-fip-announcer/commit/016f69352180da9bb1ee8cbc7e8ce3b45dc7ecf8))
* probe duration metric ([2e99db3](https://github.com/TobiasMende/hcloud-fip-announcer/commit/2e99db3931f16d0005160ca1b955e796bc645ca0)), closes [#6](https://github.com/TobiasMende/hcloud-fip-announcer/issues/6)


### Bug Fixes

* **deps:** update module github.com/onsi/ginkgo/v2 to v2.33.1 ([d6b9197](https://github.com/TobiasMende/hcloud-fip-announcer/commit/d6b919727cb0c80bd6035df3a77d61df51858513))
* **deps:** update module github.com/onsi/ginkgo/v2 to v2.33.1 ([e8c19ac](https://github.com/TobiasMende/hcloud-fip-announcer/commit/e8c19acce1446e9c806cc1ce80c28300bc3afdb8))
* **deps:** update module github.com/onsi/gomega to v1.44.0 ([a9dbf0f](https://github.com/TobiasMende/hcloud-fip-announcer/commit/a9dbf0fecc14ff2005ade334197ed847fafec7fe))
* **deps:** update module github.com/onsi/gomega to v1.44.0 ([e603992](https://github.com/TobiasMende/hcloud-fip-announcer/commit/e603992ea210297b3c54cf187c71e46b36208208))
* persist pending moves across leader changes ([5314bc8](https://github.com/TobiasMende/hcloud-fip-announcer/commit/5314bc843409478081a6ca7e58280f8ac8fd6455))
* persist pending moves across leader changes ([1b15b1d](https://github.com/TobiasMende/hcloud-fip-announcer/commit/1b15b1d7007198805435227969b6d32c039137b9)), closes [#7](https://github.com/TobiasMende/hcloud-fip-announcer/issues/7)

## [0.2.0](https://github.com/TobiasMende/hcloud-fip-announcer/compare/v0.1.0...v0.2.0) (2026-10-23)


### Features

* prefer nodes in the Floating IP's home location ([1cb0430](https://github.com/TobiasMende/hcloud-fip-announcer/commit/1cb043068d6e97f0aaac13969ec38508ed824b23))
* prefer nodes in the Floating IP's home location ([19de838](https://github.com/TobiasMende/hcloud-fip-announcer/commit/19de838949de26ee6d5d130541a468f4f759dc4d)), closes [#15](https://github.com/TobiasMende/hcloud-fip-announcer/issues/15)


### Bug Fixes

* **ci:** main guard without git fetch ([6d9ad70](https://github.com/TobiasMende/hcloud-fip-announcer/commit/6d9ad702d194e1dc1bc76e792a771ed6d548e361))
* **ci:** main guard without git fetch ([b476b7b](https://github.com/TobiasMende/hcloud-fip-announcer/commit/b476b7b3d391a1df7c1b3abaa7c849a125d6e6aa))
* **deps:** update kubernetes modules ([0d433c4](https://github.com/TobiasMende/hcloud-fip-announcer/commit/0d433c4f7137f8f49e51f04d26a06b5be14dea32))
* **deps:** update kubernetes modules ([e735247](https://github.com/TobiasMende/hcloud-fip-announcer/commit/e735247f30bbc80c1545b3becfd0e15b3cf62f8c))

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
