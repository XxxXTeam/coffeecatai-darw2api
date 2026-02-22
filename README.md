# CoffeeCat AI Draw2API

将 [CoffeeCat AI](https://www.coffeecatai.com) 的绘图能力转换为兼容 OpenAI API 的接口，可直接对接支持 OpenAI 格式的客户端（如 ChatGPT-Next-Web、LobeChat 等）。

## 功能概览

| 模型名 | 类型 | 说明 |
|--------|------|------|
| `banana` | 文生图 | 默认比例 3:2，SD1.5 Anime 模型 |
| `banana-{比例}` | 文生图 | 指定比例，如 `banana-16:9`、`banana-1:1` |
| `banana-optimize` | 提示词优化 | 输入简短描述，输出详细的生图提示词 |
| `banana-describe` | 图片描述 | 输入图片，输出英文描述/反向提示词 |
| `banana-upscale` | 图片超分 | 默认 UltraSharp V2（4x，2K） |
| `banana-upscale-ultrasharp` | 图片超分 | UltraSharp V2，4x 放大，10-20s |
| `banana-upscale-animesharp` | 图片超分 | AnimeSharp V4，2x 放大，2-3s |
| `banana-upscale-realesrgan` | 图片超分 | Real-ESRGAN，4x 放大，6-10s |
| `banana-upscale-seedvr` | 图片超分 | SeedVR2，高级模型，30-90s |

### 支持的图片比例

`1:1` `2:3` `3:2` `3:4` `4:3` `4:5` `5:4` `9:16` `16:9` `21:9`

## 快速开始

### 1. 配置

复制 `config.example.json` 为 `config.json` 并编辑：

```json
{
  "port": 8080,
  "api_keys": [],
  "solver_url": "http://127.0.0.1:5072",
  "solver_dir": "",
  "workers": 2,
  "browsers": 2,
  "pool_min": 1,
  "pool_max": 6
}
```

| 字段 | 说明 |
|------|------|
| `port` | 服务监听端口 |
| `api_keys` | API Key 列表，为空则不鉴权 |
| `solver_url` | Turnstile 验证码求解器地址（已有外部求解器时填写） |
| `solver_dir` | Solver 目录路径（留空自动检测 `./solver`） |
| `workers` | 验证码求解并发数 |
| `browsers` | Solver 浏览器实例数 |
| `pool_min` | 令牌池最小数量 |
| `pool_max` | 令牌池最大数量 |

### 2. 运行

```bash
# 直接运行（自动启动内置 solver）
./coff

# 或手动启动 solver 后运行
cd solver && uv run api_solver.py --browser_type camoufox --thread 3
./coff
```

### 3. Docker 部署

```bash
docker run -d \
  -p 8080:8080 \
  -v ./config.json:/app/config.json \
  ghcr.io/xxxxteam/coffeecatai-darw2api:latest
```

## API 接口

### 文生图

**POST** `/v1/images/generations`

```json
{
  "model": "banana-16:9",
  "prompt": "a cute cat sitting on a windowsill",
  "n": 1,
  "size": "1344x768"
}
```

### 图生图

**POST** `/v1/images/generations`

```json
{
  "model": "banana",
  "prompt": "transform to anime style",
  "image": "data:image/jpeg;base64,..."
}
```

`image` 字段支持 base64 和 URL 两种格式。

### 提示词优化

**POST** `/v1/chat/completions`

```json
{
  "model": "banana-optimize",
  "messages": [
    {"role": "user", "content": "一只可爱的猫咪"}
  ]
}
```

返回优化后的详细提示词文本。

### 图片描述（反向提示词）

**POST** `/v1/chat/completions`

```json
{
  "model": "banana-describe",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "text", "text": "describe this image"},
        {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}}
      ]
    }
  ]
}
```

### 图片超分

**POST** `/v1/chat/completions`

```json
{
  "model": "banana-upscale-animesharp",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "text", "text": "upscale"},
        {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}}
      ]
    }
  ]
}
```

超分模型返回 Markdown 图片链接。

### 模型列表

**GET** `/v1/models`

返回所有可用模型。

## 鉴权

在 `config.json` 中配置 `api_keys` 后，请求需携带 `Authorization: Bearer <your-key>` 头。

## 许可证

[GPL-3.0](LICENSE)
