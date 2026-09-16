# 配置与运行安全

本地 Ollama 的 `api_key: ollama` 只是兼容字段，不是真实密钥。远程服务必须使用环境变量：

```yaml
openai:
  api_key: "${OPENAI_API_KEY}"
```

PowerShell 中通过 `$env:OPENAI_API_KEY = "实际密钥"` 设置。

项目已忽略 `.env`、`data/`、Checkpoint 和索引清单。工具审计日志会隐藏
`api_key`、`access_token`、`client_secret`、`password`、`authorization`、
Bearer Token 及 URL 中的常见密钥参数。Session/Checkpoint 只保存对话和运行状态，
不会从配置读取并写入 API Key；同时不要把真实密钥作为聊天内容发送给 Agent。

生产部署还应限制服务监听地址、通过反向代理启用 TLS 和身份认证，并定期轮换密钥。

## 本地文件读取

`local_file_read` 只读取 `config.yaml` 中 `local_files.roots` 明确列出的目录。
默认模板开放项目的 `workspace-files`；当前开发配置额外开放了 `E:\11\work`，并限制
为不超过 10 MiB 的 UTF-8 文本或 DOCX 正文；目录、
二进制文件、越界路径和通过符号链接跳出授权目录的请求都会被拒绝。读取不会修改
文件，因此不触发人工审批，但仍会经过工具超时和审计日志。

需要读取其他目录时，应添加尽量具体的绝对路径：

```yaml
local_files:
  enabled: true
  roots:
    - "workspace-files"
    - 'C:\Users\Administrator\Documents\project-docs'
  max_bytes: 10485760
```

不要开放 `C:\`、整个用户目录、密钥目录或浏览器配置目录。完整 Docker 模式默认
只能读取 Compose 以只读方式挂载的 `workspace-files`；宿主机其他目录需要另行添加
只读 volume，并使用对应的容器内路径配置 `roots`。
