# CoffeeCat AI Draw2API

将 [CoffeeCat AI](https://www.coffeecatai.com) 的绘图能力转换为兼容 OpenAI API 的接口，可直接对接支持 OpenAI 格式的客户端（如 ChatGPT-Next-Web、LobeChat 等）。

## 生图模型

| 模型名 | 说明 | 状态 |
|--------|------|------|
| `zimage-turbo` | Z-Image Turbo，速度与质量兼顾 | 免费 |
| `zimage-turbo-nsfw` | Z-Image NSFW LoRa 增强 | 需要积分（401） |
| `zimage-base-vip` | Z-Image Base，最新最强 | 需要积分（401） |
| `flux2klein` | Flux.2 Klein 4B，极速生成 | 免费 |
| `banana` | Nano Banana，Gemini 2.5 驱动 | 需要积分（401） |
| `banana-2` | Nano Banana 2，Gemini Flash | 需要积分（401） |
| `banana-pro` | Nano Banana Pro，Gemini 2.5 驱动 | 需要积分（401） |

> 如果请求返回 **401 Unauthorized**，说明该模型需要 CoffeeCat 账户积分才能使用。目前已知 `zimage-turbo` 和 `flux2klein` 为免费模型。

每个模型均可通过 `模型名-比例` 的格式指定输出尺寸，如 `zimage-turbo-16:9`、`banana-1:1`。

### 支持的图片比例

**zimage 系列** / **flux2klein**（5 种）：

| 比例 | 分辨率 |
|------|--------|
| `1:1` | 1024x1024 |
| `3:4` | 768x1024 |
| `4:3` | 1024x768 |
| `9:16` | 720x1280 |
| `16:9` | 1280x720 |

**banana 系列**（10 种）：

| 比例 | 分辨率 |
|------|--------|
| `1:1` | 1024x1024 |
| `2:3` | 832x1248 |
| `3:2` | 1248x832 |
| `3:4` | 864x1184 |
| `4:3` | 1184x864 |
| `4:5` | 896x1152 |
| `5:4` | 1152x896 |
| `9:16` | 768x1344 |
| `16:9` | 1344x768 |
| `21:9` | 1536x672 |

## Kissing / Hugging（图生图）

上传 1-2 张人物照片，生成亲吻或拥抱效果图。使用 zimage 系列分辨率（5 种），默认 3:4。

| 模型名 | 说明 | 状态 |
|--------|------|------|
| `kissing` | 亲吻效果，默认 3:4 | 需要积分 |
| `kissing-{比例}` | 指定比例，如 `kissing-1:1` | 需要积分 |
| `hugging` | 拥抱效果，默认 3:4 | 免费 |
| `hugging-{比例}` | 指定比例，如 `hugging-16:9` | 免费 |

> 上传 1 张图片时为单人模式；上传 2 张图片时为双人模式（将两个不同人物合成）。Prompt 作为动作/场景描述，例如 `kissing romantically on the bow of a ship`。

## 辅助功能

| 模型名 | 类型 | 说明 |
|--------|------|------|
| `zimage-turbo-optimize` | 提示词优化 | 输入简短描述，输出详细的生图提示词 |
| `zimage-turbo-describe` | 图片描述 | 输入图片，输出英文描述/反向提示词 |
| `zimage-turbo-upscale` | 图片超分 | 默认 UltraSharp V2（4x，2K） |
| `zimage-turbo-upscale-ultrasharp` | 图片超分 | UltraSharp V2，4x 放大，10-20s |
| `zimage-turbo-upscale-animesharp` | 图片超分 | AnimeSharp V4，2x 放大，2-3s |
| `zimage-turbo-upscale-realesrgan` | 图片超分 | Real-ESRGAN，4x 放大，6-10s |
| `zimage-turbo-upscale-seedvr` | 图片超分 | SeedVR2，高级模型，30-90s |

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
  "model": "zimage-turbo-16:9",
  "prompt": "a cute cat sitting on a windowsill",
  "n": 1,
  "size": "1280x720"
}
```

### 图生图

**POST** `/v1/images/generations`

```json
{
  "model": "zimage-turbo",
  "prompt": "transform to anime style",
  "image": "data:image/jpeg;base64,..."
}
```

`image` 字段支持 base64 和 URL 两种格式。

### 提示词优化

**POST** `/v1/chat/completions`

```json
{
  "model": "zimage-turbo-optimize",
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
  "model": "zimage-turbo-describe",
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
  "model": "zimage-turbo-upscale-animesharp",
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
