# Custom Providers Guide

Vix ships with built-in support for Anthropic, OpenAI, OpenRouter, Bedrock, Ollama, and more.
You can add your own providers — or override settings of the built-ins — without touching the
binary. Everything is driven by a plain JSON file you drop into `~/.vix/`.

---

## How it works

Vix has an embedded `providers.json` baked into the binary. At startup it looks for overlay files
in this order and merges them on top:

1. `~/.vix/providers.json` — your user-global overrides
2. `./.vix/providers.json` — project-level overrides (wins over the above)

**Merge rules:**
- A provider entry with the same `id` as a built-in is **field-patched** — only the fields you
  supply in your overlay win; everything else stays as the built-in defines it.
- A provider entry with a new `id` is **appended** after all built-ins.
- The `models` and `credential_methods` arrays **replace wholesale** when present in the overlay
  (they are not merged entry-by-entry).

After changing your overlay file, restart the daemon for the changes to take effect:

```bash
vix daemon stop && vix daemon start
```

---

## Quick start — add a custom OpenAI-compatible provider

Create `~/.vix/providers.json`:

```json
{
  "schema_version": 1,
  "providers": [
    {
      "id": "my-provider",
      "display_name": "My Provider",
      "model_prefix": "my-provider",
      "wire_format": "chat_completions",
      "local": true,
      "inference": {
        "base_url": "https://my-api.example.com/v1",
        "auth_scheme": "bearer"
      },
      "credential_methods": [
        { "kind": "api_key", "env_var": "MY_PROVIDER_API_KEY" }
      ],
      "models": []
    }
  ],
  "auth_logins": []
}
```

Then set your API key:

```bash
export MY_PROVIDER_API_KEY=sk-...
```

Restart `vixd` and your provider will appear in the model picker. If `"local": true` is set,
the model list is fetched live from the server — see [Model discovery](#model-discovery) below.

---

## Full field reference

### Top-level document

| Field | Type | Overlay required? | Description |
|---|---|---|---|
| `schema_version` | `int` | No | If present, must be `1`. Newer values are rejected. Can be omitted from overlays — the embedded base already carries it. |
| `providers` | `array` | No | List of provider specs. Can be `[]` if you only need to add `auth_logins`. |
| `auth_logins` | `array` | No | OAuth login specs. Leave as `[]` unless you need OAuth. |

---

### Provider spec (`providers[*]`)

The "Required" column below applies to **new providers** you are adding from scratch.
When **patching an existing built-in**, only supply the fields you want to override — all
others are inherited from the embedded spec.

| Field | Type | New provider | Description |
|---|---|---|---|
| `id` | `string` | Yes | Unique stable identifier. For patches, must match the existing provider's `id` exactly. |
| `display_name` | `string` | Yes | Human-readable name shown in the TUI. |
| `model_prefix` | `string` | Yes | The prefix used in model specs (e.g. `"my-provider"` → spec `"my-provider/model-name"`). Must not contain `/`. Must not collide with another provider's prefix. |
| `wire_format` | `string` | Yes | Selects the HTTP adapter. See [Wire formats](#wire-formats). |
| `effort_policy` | `string` | No | Default reasoning-effort policy. See [Effort policy](#effort-policy). |
| `local` | `bool` | No | When `true`, models are fetched live from the server instead of the static `models` array. See [Model discovery](#model-discovery). Note: once set to `true` in the embedded defaults it cannot be overridden back to `false` by an overlay. |
| `inference` | `object` | Yes | Connection settings. See [Inference spec](#inference-spec-inference). |
| `credential_methods` | `array` | Yes | Ordered list of ways to obtain a credential. The first one that resolves wins. **Replaces wholesale** in overlays — not merged entry-by-entry. See [Credential methods](#credential-methods-credential_methods). |
| `models` | `array` | No | Static model catalogue. Ignored when `local: true`. **Replaces wholesale** in overlays. See [Model list](#model-list-models). |

---

### Wire formats (`wire_format`)

Selects which compiled HTTP adapter is used. This is a closed set — any other value is rejected at load.

| Value | Description |
|---|---|
| `"chat_completions"` | OpenAI-compatible Chat Completions API. Use this for any OpenAI-compatible endpoint (OpenRouter, Ollama, llama.cpp, vLLM, LM Studio, or any third-party proxy). |
| `"messages"` | Anthropic Messages API. Use only for Anthropic-native endpoints. |
| `"responses"` | OpenAI Responses API. Use only for official OpenAI endpoints. |

**When in doubt, use `"chat_completions"`** — it is the de facto standard for self-hosted and
third-party LLM servers.

---

### Effort policy (`effort_policy`)

Controls what default reasoning effort is used when you don't specify one explicitly.

| Value | Description |
|---|---|
| `""` (empty or omitted) | No reasoning effort is sent. Good default for most providers. |
| `"adaptive"` | Always sends `effort: "adaptive"` (Anthropic-style). |
| `"openai_reasoning"` | Sends `reasoning_effort: "medium"` for known reasoning models (o1, o3, o4, gpt-5, *-thinking); nothing for standard models. |

---

### Inference spec (`inference`)

| Field | Type | Description |
|---|---|---|
| `base_url` | `string` | The API root URL. Supports `${env:VAR}` and `${env:VAR:-default}` interpolation. See [Env var interpolation](#environment-variable-interpolation). |
| `auth_scheme` | `string` | How the resolved credential is attached to every request. `"bearer"` → `Authorization: Bearer <value>`. `"x-api-key"` → `x-api-key: <value>` (Anthropic default). |
| `auth_header` | `string` | Raw header name for non-standard auth schemes. Rarely needed — leave empty unless the provider docs say otherwise. |
| `headers` | `object` | Static `"key": "value"` headers added to every request. Values support `${env:VAR}` interpolation; entries whose value resolves to an empty string are dropped. |
| `query_params` | `object` | Static `"key": "value"` query parameters appended to every request URL. Values support `${env:VAR}` interpolation; entries whose value resolves to an empty string are dropped. |
| `json_set` | `object` | Arbitrary `"key": value` fields injected into every request body. Values are **not** interpolated (they are non-string JSON). |
| `effort_style` | `string` | For `chat_completions` wire format only. `"reasoning_effort"` sends the standard OpenAI `reasoning_effort` knob. `"reasoning_split"` sends `reasoning_split: true`. Leave empty for no reasoning field. |

---

### Credential methods (`credential_methods`)

An ordered list. Vix tries each entry in order and uses the first one that resolves to a non-empty
value.

#### `kind: "api_key"` — static API key (most common)

```json
{ "kind": "api_key", "env_var": "MY_API_KEY", "keyring": "my-api-key" }
```

| Field | Description |
|---|---|
| `env_var` | Environment variable name to read. Checked first. At least one of `env_var` or `keyring` **must** be set for `api_key` methods. |
| `keyring` | OS keyring / `.env` file key name. Checked if `env_var` is unset or empty. At least one of `env_var` or `keyring` **must** be set for `api_key` methods. |
| `label` | Human-readable name shown in the credential panel (useful when a provider has multiple keys of the same kind). Duplicate labels within the same provider are rejected. |
| `header_style` | `"bearer"` to force `Authorization: Bearer` regardless of the `auth_scheme` on the inference block. |
| `extra_headers_producer` | Built-in function that derives extra headers from the credential value. `"anthropic_oauth"` or `"codex_oauth"`. Leave empty for normal API keys. |
| `base_url` | Endpoint override implied by this credential method (e.g. OAuth uses a different base URL than API key). Must be HTTPS for remote providers. |
| `requires_base_url` | `true` if the user must supply the base URL at credential-entry time (e.g. region-specific endpoints). The TUI will prompt for it. **Requires `keyring` to also be set** (used to store the endpoint). |
| `base_url_env` | Env var that overrides the stored user-supplied base URL for a `requires_base_url` method. |

#### `kind: "none"` — no credential needed

```json
{ "kind": "none" }
```

For local servers that run without authentication (Ollama without an API key, llama.cpp without
`--api-key`). Resolves to a fixed placeholder value. Vix will never prompt for a key.

> **Tip:** Put `none` as the *last* entry in `credential_methods` alongside an `api_key` entry.
> That way the provider works both with and without a key:
> ```json
> "credential_methods": [
>   { "kind": "api_key", "env_var": "MY_API_KEY" },
>   { "kind": "none" }
> ]
> ```

#### `kind: "oauth_mint_key"` / `kind: "oauth_token"` — OAuth

These reference an `auth_logins` entry by `login_id`. Only relevant when a provider's
authentication uses an OAuth browser flow rather than a static API key.

```json
{ "kind": "oauth_token", "login_id": "my-oauth-login" }
```

| Kind | Description |
|---|---|
| `oauth_token` | PKCE flow → refreshable bearer access token (e.g. Anthropic). |
| `oauth_mint_key` | PKCE flow → minted user API key, stored like a static key (e.g. OpenRouter). |

> **Important:** The `flow` values in `auth_logins` are a **closed compiled set** (see
> [OAuth login spec](#oauth-login-spec-auth_logins)). You cannot add a fully new OAuth
> flow by writing JSON alone — the implementation lives in `internal/auth/`. What you
> *can* do via JSON is override values on an existing login (callback port, scope,
> redirect URI, etc.) by providing an `auth_logins` entry with the same `id`.

See [OAuth login spec](#oauth-login-spec-auth_logins) for the full `auth_logins` field reference.

---

### Model list (`models`)

Static entries shown in the model picker. **Ignored entirely when `"local": true`** — the model
list is fetched live from the server instead.

```json
"models": [
  { "spec": "my-provider/fast-model",  "display_name": "Fast Model",  "context_window": 128000 },
  { "spec": "my-provider/smart-model", "display_name": "Smart Model", "context_window": 1000000 }
]
```

| Field | Type | Description |
|---|---|---|
| `spec` | `string` | Full prefixed model identifier used everywhere in config (e.g. `"my-provider/model-name"`). Must be `<model_prefix>/<bare-model-id>`. |
| `display_name` | `string` | Human-readable name shown in the picker. |
| `context_window` | `int` | Input context window in tokens. `0` or omitted means unknown — shown as `—` in the TUI and disables auto-compaction. |

---

## Model discovery

When `"local": true`, the model list is fetched live from the server on every TUI open
(cached for 5 seconds). No static `models` array is needed. This works for any server that
exposes an OpenAI-compatible `GET /models` endpoint.

**What the daemon does:**

1. `GET {base_url}/models` — discovers all installed/available models
2. **Ollama only:** `GET /api/ps` marks which models are currently loaded in RAM; `POST /api/show`
   per model fetches each model's context window length
3. **llama.cpp only:** `GET /props` fetches the serving context length (`n_ctx`)

If the server is unreachable, the provider appears as offline in the TUI — no error is thrown.
The probe times out after 1.5 seconds.

**Use `"local": true` when:**
- You're pointing at a self-hosted server (Ollama, llama.cpp, vLLM, LM Studio, your own proxy…)
- The model list changes frequently (you pull/delete models)
- You don't want to maintain a static model list by hand

**Use a static `models` array (omit `local`) when:**
- You're pointing at a cloud API with a stable, known model catalogue
- The server does not expose `/models` in OpenAI-compatible format

> **Note:** `"local": true` is inherited through the merge. If you're overriding a built-in
> provider that already has `"local": true` (like Ollama), you don't need to re-declare it —
> it can never be set back to `false` by an overlay.

---

## Common recipes

### Override Ollama's base URL (e.g. remote Ollama server)

```json
{
  "schema_version": 1,
  "providers": [
    {
      "id": "ollama",
      "inference": {
        "base_url": "http://my-remote-machine:11434/v1"
      }
    }
  ],
  "auth_logins": []
}
```

Only `base_url` changes. `"local": true` is inherited — models are still discovered live.

---

### Add a cloud provider with a static model list

```json
{
  "schema_version": 1,
  "providers": [
    {
      "id": "my-cloud",
      "display_name": "My Cloud",
      "model_prefix": "my-cloud",
      "wire_format": "chat_completions",
      "inference": {
        "base_url": "https://api.example.com/v1",
        "auth_scheme": "bearer"
      },
      "credential_methods": [
        { "kind": "api_key", "env_var": "MY_CLOUD_API_KEY" }
      ],
      "models": [
        { "spec": "my-cloud/fast",  "display_name": "Fast",  "context_window": 128000 },
        { "spec": "my-cloud/smart", "display_name": "Smart", "context_window": 200000 }
      ]
    }
  ],
  "auth_logins": []
}
```

---

### Add a self-hosted server with live model discovery

```json
{
  "schema_version": 1,
  "providers": [
    {
      "id": "my-local",
      "display_name": "My Local Server",
      "model_prefix": "my-local",
      "wire_format": "chat_completions",
      "local": true,
      "inference": {
        "base_url": "http://localhost:8080/v1",
        "auth_scheme": "bearer"
      },
      "credential_methods": [
        { "kind": "api_key", "env_var": "MY_LOCAL_API_KEY" },
        { "kind": "none" }
      ],
      "models": []
    }
  ],
  "auth_logins": []
}
```

`credential_methods` tries the env var first, then falls back to `none` — so the provider works
whether or not your server requires a key.

---

### Add static headers to every request

```json
"inference": {
  "base_url": "https://api.example.com/v1",
  "auth_scheme": "bearer",
  "headers": {
    "X-Project-ID": "proj_123",
    "X-Custom-Header": "my-value"
  }
}
```

---

### Inject extra fields into every request body

Some providers require non-standard body fields:

```json
"inference": {
  "base_url": "https://api.example.com/v1",
  "auth_scheme": "bearer",
  "json_set": {
    "custom_field": "value"
  }
}
```

---

## Environment variable interpolation

The following `inference` string fields support `${env:...}` substitution:
`base_url`, and individual values inside `headers` and `query_params`.

> **Not interpolated:** `json_set` values (they are arbitrary JSON, not strings).

| Syntax | Behaviour |
|---|---|
| `${env:MY_VAR}` | Substituted with `$MY_VAR`. Empty string if the variable is unset. |
| `${env:MY_VAR:-https://fallback.example.com/v1}` | Substituted with `$MY_VAR`, falling back to the default when unset or empty. |

Headers and query params whose value resolves to an empty string are **dropped** from the
request (e.g. an optional group-ID header won't be sent as a blank header when the env var
is absent).

Example:

```json
"inference": {
  "base_url": "${env:MY_BASE_URL:-https://ai.example.com/v1}",
  "headers": {
    "X-Group-ID": "${env:MY_GROUP_ID}"
  }
}
```

---

## OAuth login spec (`auth_logins`)

`auth_logins` entries define the OAuth browser flow used by `oauth_token` and
`oauth_mint_key` credential methods. Each entry is referenced by its `id` from a
`credential_methods` entry via `login_id`.

### Available flows (`flow`)

The `flow` field is a **closed set** — only the values below are compiled in. Any other
value is rejected at load.

| Value | Description |
|---|---|
| `"oauth_pkce_token"` | PKCE auth-code flow → bearer access token. Used by Anthropic. |
| `"oauth_pkce_mint"` | PKCE auth-code flow → minted user API key. Used by OpenRouter. |
| `"oauth_codex"` | ChatGPT/Codex combined browser + device-code flow. Used by OpenAI Codex. |

### `auth_logins[*]` field reference

| Field | Type | Description |
|---|---|---|
| `id` | `string` | Unique identifier. Matched against `login_id` in `credential_methods`. |
| `flow` | `string` | OAuth flow implementation to use. See table above. |
| `client_id` | `string` | OAuth application client ID (plaintext). |
| `client_id_b64` | `string` | OAuth application client ID, base64-encoded (used when the ID contains binary or sensitive bytes). |
| `authorize_url` | `string` | Authorization endpoint URL (the browser redirect target). |
| `token_url` | `string` | Token exchange endpoint URL. |
| `keys_url` | `string` | Key-minting endpoint URL (`oauth_pkce_mint` only). |
| `callback_port` | `int` | Localhost port for the redirect URI callback listener. |
| `callback_path` | `string` | Path for the redirect URI callback (e.g. `"/oauth/callback"`). |
| `redirect_uri` | `string` | Full redirect URI sent to the authorization server. Usually derived from `callback_port` + `callback_path`, but can be set explicitly. |
| `scope` | `string` | Space-separated OAuth scopes to request. |
| `originator` | `string` | Internal tag identifying who initiated the flow (optional, cosmetic). |
| `extra_authorize_params` | `object` | Additional `key: value` query parameters appended to the authorization URL. |
| `device` | `object` | RFC 8628 device-code endpoints for headless flows. See [DeviceSpec](#devicespec-device) below. |

### DeviceSpec (`device`)

Only relevant for `oauth_codex`. Provides a fallback device-code path for environments
where opening a browser is impossible.

| Field | Type | Description |
|---|---|---|
| `user_code_url` | `string` | URL shown to the user to enter the device code. |
| `token_url` | `string` | Token polling endpoint. |
| `verification_uri` | `string` | Verification URI returned by the device-code endpoint. |
| `redirect_uri` | `string` | Redirect URI used by the device flow. |
| `timeout_seconds` | `int` | How long to poll before giving up. |

### Security constraint: auth host allowlist

> **Important:** OAuth endpoint URLs (`authorize_url`, `token_url`, `keys_url`, device
> URLs) are validated against a **hardcoded allowlist** of trusted hosts. You cannot point
> an `auth_logins` entry at an arbitrary domain — doing so is rejected at load with an
> `"auth host not in allowlist"` error. The allowlist as of this writing:
> - `claude.ai`, `platform.claude.com`, `api.anthropic.com`
> - `auth.openai.com`
> - `openrouter.ai`
>
> This is a deliberate security boundary: a data-driven config file cannot redirect OAuth
> token exchange to an attacker-controlled host. Changing the allowlist requires modifying
> `internal/providers/validate.go` and rebuilding the binary.

### Annotated example — OpenRouter OAuth (pkce_mint)

This is how the built-in OpenRouter OAuth login is structured. You can patch any field by
providing an entry with `"id": "openrouter"` in your overlay's `auth_logins` — only the
fields you supply win, the rest are inherited from the embedded spec.

```json
{
  "providers": [],
  "auth_logins": [
    {
      "id": "openrouter",
      "flow": "oauth_pkce_mint",
      "authorize_url": "https://openrouter.ai/auth",
      "keys_url": "https://openrouter.ai/api/v1/auth/keys",
      "callback_port": 53781,
      "callback_path": "/callback"
    }
  ]
}
```

> **Tip:** When in doubt, leave `auth_logins` as `[]` in your overlay. OAuth flows are
> rarely needed for BYOP use cases — most self-hosted and third-party providers use a
> plain API key.

---

## Troubleshooting

**Provider doesn't appear in the TUI**
- Restart `vixd` — the registry is loaded once at startup.
- Check the `vixd` log output on startup for a `[providers] using embedded defaults: ...` warning.
- Note: **unknown or misspelled field names are silently ignored** (standard Go JSON behaviour
  — `json.Unmarshal` does not reject unknown fields). If you mistype `"wire_fromat"` it will
  silently be ignored and the field will keep its inherited value. Double-check your field names
  against this document.

> **Parse error behavior:** On a parse error in your overlay, `vixd` logs a warning
> (`[providers] using embedded defaults: ...`) and continues starting with the built-in
> embedded providers — your custom providers simply won't be loaded. No crash, no silent
> corruption. Fix the JSON and restart `vixd` to pick up your overlay.

**"model spec must start with one of: ..." error**
- The `model_prefix` in your spec must match the prefix you set in the overlay exactly, including
  case. Model specs are always `<model_prefix>/<bare-model-id>`.

**Local provider shows as offline**
- The `base_url` is unreachable from the machine running `vixd`.
- The server may not expose `/models` in OpenAI-compatible format — check the server docs.
- The probe times out after 1.5 seconds; a slow server may appear offline even if it's running.

**API key is not picked up**
- The env var is resolved at inference time, not at startup — make sure it's set in the
  environment where `vixd` runs (not just the shell running `vix`).
- If `keyring` is specified, vix also checks the `.env` file at the project root.

