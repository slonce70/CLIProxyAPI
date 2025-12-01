# Factory Droid - Інструкція з використання

## Що це?

Factory Droid інтеграція дозволяє використовувати вашу підписку Factory для доступу до різних AI моделей через OpenAI-сумісний API. Це працює аналогічно до інших провайдерів в CLIProxyAPI (Claude, Gemini, Codex).

## Доступні моделі

| ID моделі | Опис |
|-----------|------|
| `droid-claude-opus-4.5` | Claude Opus 4.5 - найпотужніша модель |
| `droid-claude-sonnet-4.5` | Claude Sonnet 4.5 - баланс швидкості та якості |
| `droid-claude-haiku-4.5` | Claude Haiku 4.5 - найшвидша |
| `droid-gpt-5.1-codex` | GPT-5.1 Codex |
| `droid-gemini-3-pro` | Gemini 3 Pro |
| `droid-glm-4.6` | GLM-4.6 |

---

## Налаштування

### Крок 1: Отримайте Factory API ключ

1. Зайдіть на https://app.factory.ai/
2. Перейдіть в Settings → API Keys
3. Створіть новий ключ (починається з `fk-...`)

### Крок 2: Встановіть Droid CLI

Переконайтесь що `droid` CLI встановлено та працює:

```bash
droid --version
```

### Крок 3: Налаштуйте config.yaml

Створіть або відредагуйте `config.yaml` в корені проекту:

```yaml
# Порт сервера
port: 8317

# API ключі для авторизації клієнтів (будь-які ваші ключі)
api-keys:
  - "my-secret-key-1"
  - "my-secret-key-2"

# Режим налагодження
debug: true

# Factory Droid API ключі
# Можна додати декілька ключів для load balancing (як у claude/gemini/codex)
droid-api-key:
  - api-key: "fk-ВАШ-ПЕРШИЙ-КЛЮЧ"
  - api-key: "fk-ВАШ-ДРУГИЙ-КЛЮЧ"   # опціонально
```

**Примітка:** Так, можна використовувати декілька API ключів! Це працює аналогічно до інших провайдерів:

```yaml
# Приклад з декількома ключами (для порівняння)
claude-api-key:
  - api-key: "sk-ant-key1"
  - api-key: "sk-ant-key2"

gemini-api-key:
  - api-key: "AIzaSy...01"
  - api-key: "AIzaSy...02"

codex-api-key:
  - api-key: "sk-key1"
  - api-key: "sk-key2"

# Droid працює так само:
droid-api-key:
  - api-key: "fk-key1"
  - api-key: "fk-key2"
```

---

## Запуск сервера

### Варіант 1: З вихідного коду

```bash
# Збілдити
go build -o cliproxyapi ./cmd/server

# Запустити
./cliproxyapi
```

### Варіант 2: Docker

```bash
docker-compose up -d
```

### Варіант 3: Готовий бінарник

Завантажте з releases та запустіть:

```bash
./cliproxyapi
```

Сервер запуститься на `http://localhost:8317` (або порт з config.yaml).

---

## Використання API

### Перевірити список моделей

```bash
curl http://localhost:8317/v1/models \
  -H "Authorization: Bearer my-secret-key-1"
```

### Відправити запит

```bash
curl -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer my-secret-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "droid-claude-haiku-4.5",
    "messages": [
      {"role": "user", "content": "Привіт! Як справи?"}
    ],
    "max_tokens": 100
  }'
```

### Streaming

```bash
curl -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer my-secret-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "droid-claude-sonnet-4.5",
    "messages": [{"role": "user", "content": "Напиши вірш"}],
    "stream": true
  }'
```

---

## Інтеграція з інструментами

### Python (OpenAI SDK)

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8317/v1",
    api_key="my-secret-key-1"
)

response = client.chat.completions.create(
    model="droid-claude-sonnet-4.5",
    messages=[{"role": "user", "content": "Привіт!"}]
)

print(response.choices[0].message.content)
```

### Cursor / VS Code / IDE

Налаштування:
- **Base URL:** `http://localhost:8317/v1`
- **API Key:** `my-secret-key-1` (ваш ключ з config.yaml api-keys)
- **Model:** `droid-claude-sonnet-4.5`

### LangChain

```python
from langchain_openai import ChatOpenAI

llm = ChatOpenAI(
    base_url="http://localhost:8317/v1",
    api_key="my-secret-key-1",
    model="droid-claude-opus-4.5"
)

response = llm.invoke("Привіт!")
print(response.content)
```

---

## Як це працює?

```
Клієнт → CLIProxyAPI → droid exec CLI → Factory API → AI модель
         (OpenAI API)   (subprocess)
```

1. Ви відправляєте запит у форматі OpenAI API
2. CLIProxyAPI конвертує його в промпт для `droid exec`
3. `droid exec` виконується як subprocess з вашим Factory API ключем
4. Відповідь конвертується назад у формат OpenAI API

---

## FAQ

### Чому затримка 1-3 секунди?

Кожен запит запускає новий `droid exec` процес. Це накладні витрати на subprocess.

### Чи можу я мати декілька API ключів?

Так! Як і для claude/gemini/codex, можна додати декілька ключів:

```yaml
droid-api-key:
  - api-key: "fk-key1"
  - api-key: "fk-key2"
  - api-key: "fk-key3"
```

Сервер буде чергувати між ними для load balancing.

### Де зберігається мій API ключ?

В `config.yaml` локально. Файл не комітьте в git - він вже в `.gitignore`.

### Які моделі доступні?

Залежить від вашої підписки Factory. Базові моделі в інтеграції:
- Claude 4.5 (Opus, Sonnet, Haiku)
- GPT-5.1 Codex
- Gemini 3 Pro
- GLM-4.6

---

## Troubleshooting

### "droid not found"

Встановіть Droid CLI та додайте до PATH:
```bash
# Перевірте
which droid
droid --version
```

### "Authentication failed"

Перевірте що ваш Factory API ключ валідний:
```bash
droid exec -p "test" -o json
```

### Пусті моделі в /v1/models

Перевірте що `droid-api-key` правильно налаштовано в `config.yaml` та перезапустіть сервер.
