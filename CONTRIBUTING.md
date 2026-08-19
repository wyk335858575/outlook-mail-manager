# 贡献指南

感谢参与改进 Outlook Mail Manager。提交前请确认改动符合项目边界：只支持个人 Microsoft 邮箱、单管理员、只收信、不保存邮箱密码、不永久删除邮件。

## 开发环境

需要 Go 1.26、Node.js 24 和 npm。安装依赖后运行：

```bash
npm --prefix web ci
npm --prefix web test
npm --prefix web run typecheck
npm --prefix web run build
go test ./...
go vet ./...
```

涉及并发、token 或同步的改动还应运行 `go test -race ./...`。涉及界面的改动应检查 1440x900 和 390x844，确认无溢出、遮挡和控制台错误。

## 提交要求

1. 每个变更只解决一个明确问题，不夹带无关重构。
2. 修复缺陷时先添加能稳定复现的测试，新功能覆盖权限和失败路径。
3. 不提交 `.env`、数据库、WAL/SHM、token、真实邮箱、日志、截图、EXE、`node_modules` 或 `web/dist`。
4. 不在日志、错误、审计或测试快照中输出 refresh token、access token、API token、TOTP secret 或主密钥。
5. 数据库变更使用新的事务迁移，并更新迁移与恢复测试。

Pull Request 应说明用户可见行为、安全影响、数据库影响、验证命令和手动验收结果。

## 发布版本

根目录 `VERSION` 是版本号来源。正式版本从 `1.0.0` 开始，每次发布只把最后一段加一；不要手动跳号。准备新版本时运行：

```bash
node scripts/version.mjs bump
# 补充 CHANGELOG.md
node scripts/version.mjs check
```

提交并推送到 `main` 后，在 GitHub Actions 中手动运行 `prepare release`。工作流会检查版本严格连续、重新执行完整测试、创建 `v$(cat VERSION)` 注释标签并触发签名发布；不要手工创建标签或直接运行底层 `release` 工作流。
