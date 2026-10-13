# 第三方组件声明

MyTizi 自身代码以 [MIT 许可证](LICENSE) 发布。打包后的 App 还附带下面这些第三方组件，它们**保留各自的许可证**，不受 MyTizi 的 MIT 许可证约束。

| 组件 | 版本 | 许可证 | 来源 |
| --- | --- | --- | --- |
| mihomo（代理核心） | v1.19.32 | GPL-3.0（以该版本标签里的 LICENSE 为准） | <https://github.com/MetaCubeX/mihomo/tree/v1.19.32> |
| meta-rules-dat（`geoip.metadb`、`geosite.dat`） | 构建当天最新 | GPL-3.0（GitHub 对该仓库 LICENSE 的识别结果） | <https://github.com/MetaCubeX/meta-rules-dat> |

## 关于 mihomo（GPL-3.0）

- App 里的 mihomo 是上游官方发布的**未修改**二进制，构建时由 `scripts/fetch-core.sh` 按 `core.lock` 下载并校验 SHA-256。
- 完整的对应源码见上表链接（该版本标签）。许可证全文随 App 附带：`MyTizi.app/Contents/Resources/licenses/mihomo-LICENSE.txt`。
- MyTizi 把 mihomo 当作**独立程序**启动，通过本机回环上的 HTTP 接口通信，并没有把它链接进自己的代码里，所以 MyTizi 自身可以使用 MIT 许可证。

## 关于地理数据库

`geoip.metadb` 与 `geosite.dat` 原样取自 meta-rules-dat 的发布，没有修改。它们由上游汇总多个规则/地址数据源生成，使用时请同时遵守上游仓库及其数据来源的许可条款。

## 升级核心时

`scripts/fetch-core.sh --update-core` 会同时记录新版本 LICENSE 的哈希；如果许可证内容发生变化，构建会失败并要求人工确认，避免在没注意的情况下改变分发条件。
