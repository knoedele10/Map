# vendor/ — 本地依赖（离线可用）

由 npm 复制构建产物，运行时不需要联网，也不需要打包步骤。

- three@0.186.1（`build/three.module.js` + `build/three.core.js`，MIT）
- lucide@1.57.0（`dist/umd/lucide.min.js`，ISC）

## 路径对照

| 引用方式 | 文件 |
| --- | --- |
| importmap `"three"` → `./vendor/three.module.js` | Three.js 核心；0.186 起 `three.module.js` 会转引同目录的 `three.core.js`，两个文件必须同时存在 |
| importmap `"three/addons/"` → `./vendor/jsm/` | OrbitControls、GLTFLoader、DRACOLoader、KTX2Loader、RoomEnvironment |
| `./vendor/lucide.min.js` | Lucide UMD 构建，挂载 `window.lucide`，用 `lucide.createIcons()` 渲染 `<i data-lucide="...">` |
| `./vendor/draco/` | Draco 解码器；`draco.setDecoderPath('./vendor/draco/')`，wasm 优先 |
| `./vendor/jsm/libs/basis/` | KTX2 / Basis 转码器；`ktx2.setTranscoderPath('./vendor/jsm/libs/basis/')` |
| `./vendor/jsm/libs/meshopt_decoder.module.js` | meshopt 解码器；`loader.setMeshoptDecoder(MeshoptDecoder)` |

## 说明

- ES module 必须经 http(s) 打开；`file://` 下浏览器会拦截模块导入，请用本地静态服务器预览。
- GLTFLoader 依赖 `jsm/utils/BufferGeometryUtils.js` 与 `jsm/utils/SkeletonUtils.js`；KTX2Loader 依赖 `jsm/utils/WorkerPool.js`、`jsm/math/ColorSpaces.js`、`jsm/libs/ktx-parse.module.js`、`jsm/libs/zstddec.module.js`，均已随包复制。
- 已校验：全部 js 文件的相对 import 均可解析，唯一裸导入为 `three`（由 importmap 映射）。
- 升级方式：`npm i three lucide`，再按上表复制对应文件即可。
- 许可证：`LICENSE.three.txt`、`LICENSE.lucide.txt`。
