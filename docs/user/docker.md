# Docker

镜像基于 `gcr.io/distroless/static:nonroot`，以非 root 用户（65532）运行，没有 shell。
仓库根目录有 `Dockerfile`；发布到镜像仓库的流程以后再做，目前请自己构建：

```bash
docker build -t e5renew .
```

## 数据和密钥

- 数据卷挂在 `/data`：配置在 `/data/config`，状态、日志和 vault 在 `/data/state`
  （环境变量 `E5RENEW_CONFIG_DIR`、`E5RENEW_DATA_DIR`）。
- 容器里没有钥匙串，主密钥通过环境变量 `E5RENEW_MASTER_KEY`（base64）传入。**推荐用 compose 的 `env_file`**：
  compose 在宿主机上以你的身份读取这个文件，再把内容作为环境变量交给容器，所以宿主机上的文件可以保持 `0600`，容器里的 65532 用户不需要读它。
  生成密钥：`docker run --rm e5renew vault init --print-key`（密钥打印在标准输出，请妥善保存）。
- 注意：环境变量能被 `docker inspect` 和有 Docker 权限的人看到（docker 组等同于 root），这和任何环境变量方案的取舍一样。
- 不推荐 compose 的 `secrets: file:`：在非 Swarm 环境里它只是把宿主机文件 bind mount 进容器，**保留宿主机上的属主和权限**，
  `0600` 的文件容器里的 65532 用户读不了。一定要用的话：`chown 65532:65532 master.key && chmod 0400 master.key`，
  文件名要对应 `e5renew-master-key`，并设置 `CREDENTIALS_DIRECTORY=/run/secrets`。
- 建议把配置里的 `vault.backend` 设为 `"file"`，这样钥匙串不可用时不会尝试访问它（`file` 后端读环境变量和 systemd 凭据）。

## compose.yaml

先在宿主机上创建 `e5renew.env`，权限 `0600`，内容一行：

```text
E5RENEW_MASTER_KEY=<base64 主密钥>
```

```yaml
services:
  e5renew:
    image: e5renew            # 自己构建的镜像
    restart: unless-stopped
    env_file:
      - ./e5renew.env
    volumes:
      - e5renew-data:/data
    # 不暴露任何端口
volumes:
  e5renew-data:
```

默认命令是 `run --log-format json`。**不要发布任何端口**：e5renew 不提供网络服务。
注意 Docker 会绕过 ufw，所以即使要发布端口也应写成 `127.0.0.1:端口:端口`。
`e5renew.env` 和 `master.key` 不要提交到 git，也不要放进镜像（`.dockerignore` 已排除）。

## 首次配置

```bash
docker compose run --rm e5renew config init
docker compose run --rm e5renew account add main --auth device_code --tenant organizations --client-id <ID>
docker compose run --rm -it e5renew account login main     # 按提示打开网址、输入设备码
docker compose run --rm e5renew run-now main
docker compose up -d
docker compose run --rm e5renew doctor
```

`account add` 需要输入密钥时，加 `-it` 以便不回显。

## 验证

- `docker compose logs -f e5renew`
- `docker compose run --rm e5renew status`（`engine:` 一行显示引擎是否在运行；与运行中的容器共用同一个数据卷）
- `docker compose run --rm e5renew doctor`：`service` 一项会报“未安装”，在容器里属于正常现象。
