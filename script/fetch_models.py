#!/usr/bin/env python3
"""Refresh internal/providers/providers.json from each provider's live catalogue.

This walks the providers defined in internal/providers/providers.json, reads
each provider's API key from the repo .env file, calls the provider's `/models`
endpoint, and rewrites each provider's `models` array with what the API returns.

Display names are auto-generated from the model id. Context windows come from the
API when reported, otherwise the existing catalogue value is kept (OpenAI's
/models exposes none). A provider with no successful fetch is emptied. Only the
`models` arrays are rewritten; the rest of the file is left byte-for-byte intact.

Standard library only — no pip installs required.

    python3 scripts/fetch_models.py
"""

import json
import os
import re
import sys
import tempfile
import urllib.error
import urllib.request

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PROVIDERS_JSON = os.path.join(REPO_ROOT, "internal", "providers", "providers.json")
ENV_FILE = os.path.join(REPO_ROOT, ".env")

# Anthropic's REST API requires a version header on every request.
ANTHROPIC_VERSION = "2023-06-01"


def load_env_file(path):
    """Parse a .env file into a dict, ignoring comments, blanks, and any line
    that isn't a bare KEY=VALUE pair (e.g. the `minimax: sk-...` notes)."""
    env = {}
    if not os.path.exists(path):
        return env
    with open(path, "r", encoding="utf-8") as fh:
        for raw in fh:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)=(.*)$", line)
            if not m:
                continue
            key, val = m.group(1), m.group(2).strip()
            if len(val) >= 2 and val[0] == val[-1] and val[0] in ("'", '"'):
                val = val[1:-1]
            env[key] = val
    return env


def interpolate(value, env):
    """Resolve ${env:VAR} and ${env:VAR:-default} references in a string using
    the supplied env dict (mirrors the Go inference layer, best-effort)."""

    def repl(match):
        body = match.group(1)
        if ":-" in body:
            name, default = body.split(":-", 1)
        else:
            name, default = body, ""
        name = name[len("env:"):] if name.startswith("env:") else name
        return env.get(name, default)

    return re.sub(r"\$\{([^}]*)\}", repl, value)


def primary_api_key_method(provider):
    """Return the canonical API-key credential method for a provider: prefer a
    plain api_key (no bearer header_style), else the first api_key method."""
    methods = provider.get("credential_methods", [])
    for m in methods:
        if m.get("kind") == "api_key" and not m.get("header_style"):
            return m
    for m in methods:
        if m.get("kind") == "api_key" and m.get("env_var"):
            return m
    return None


def build_request(provider, key, env):
    """Construct the urllib Request for a provider's /models endpoint."""
    inference = provider.get("inference", {})
    base_url = interpolate(inference.get("base_url", ""), env).rstrip("/")
    url = base_url + "/models"

    headers = {"Accept": "application/json"}
    scheme = inference.get("auth_scheme", "bearer")
    if scheme == "x-api-key":
        headers["x-api-key"] = key
        headers["anthropic-version"] = ANTHROPIC_VERSION
    else:
        headers["Authorization"] = "Bearer " + key

    return urllib.request.Request(url, headers=headers, method="GET"), url


# Field names different providers use for the input context window. Anthropic
# uses max_input_tokens; OpenRouter uses context_length; others vary. OpenAI's
# /models endpoint exposes no context window at all.
CONTEXT_FIELDS = ("max_input_tokens", "context_length", "context_window",
                  "max_context_length", "max_context_window_tokens")


def model_context(item):
    """Return the context window (int) for a model object, or None if the API
    didn't include one. Checks the known field names and a nested top_provider."""
    if not isinstance(item, dict):
        return None
    for field in CONTEXT_FIELDS:
        val = item.get(field)
        if isinstance(val, int):
            return val
    top = item.get("top_provider")
    if isinstance(top, dict) and isinstance(top.get("context_length"), int):
        return top["context_length"]
    return None


def extract_models(payload):
    """Pull (model_id, context_window) pairs out of a parsed JSON response.
    Handles the OpenAI-compatible {"data": [{"id": ...}]} shape and variants.
    context_window is None when the provider doesn't report one."""
    items = None
    if isinstance(payload, dict):
        for field in ("data", "models"):
            if isinstance(payload.get(field), list):
                items = payload[field]
                break
    elif isinstance(payload, list):
        items = payload
    if items is None:
        return None

    out = []
    for it in items:
        if isinstance(it, dict):
            mid = str(it.get("id") or it.get("name") or it)
            out.append((mid, model_context(it)))
        else:
            out.append((str(it), None))
    return out


# Substrings that mark an OpenAI model as a non-chat modality. OpenAI's /models
# endpoint carries no capability flags, so chat vs. non-chat is inferred from the
# id. A model is kept only if it looks like a chat/reasoning text model and
# matches none of these.
OPENAI_NON_CHAT_MARKERS = (
    "embedding", "tts", "whisper", "transcribe", "audio", "realtime",
    "image", "moderation", "sora", "dall-e", "search-preview",
    "babbage", "davinci",
)

# Prefixes of OpenAI chat/reasoning text model families.
OPENAI_CHAT_PREFIXES = ("gpt-", "chatgpt-", "o1", "o3", "o4", "chat-latest")


def is_openai_chat_model(model_id):
    """True if an OpenAI model id looks like a chat/reasoning text model,
    excluding embeddings, audio/TTS, image, realtime, moderation, etc."""
    mid = model_id.lower()
    if any(marker in mid for marker in OPENAI_NON_CHAT_MARKERS):
        return False
    return any(mid.startswith(p) for p in OPENAI_CHAT_PREFIXES)


def fetch_models(provider, env):
    method = primary_api_key_method(provider)
    if not method:
        return ("skip", "no API-key credential method defined", [])

    env_var = method.get("env_var", "")
    key = env.get(env_var, "")
    if not key:
        return ("skip", "no key for %s in .env" % env_var, [])

    request, url = build_request(provider, key, env)
    try:
        with urllib.request.urlopen(request, timeout=30) as resp:
            body = resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", "replace").strip()
        return ("error", "HTTP %s @ %s%s" % (e.code, url, "\n    " + detail if detail else ""), [])
    except urllib.error.URLError as e:
        return ("error", "request failed @ %s: %s" % (url, e.reason), [])

    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        return ("error", "non-JSON response @ %s" % url, [])

    models = extract_models(payload)
    if models is None:
        return ("error", "unrecognized response shape @ %s" % url, [])
    if provider.get("id") == "openai":
        models = [m for m in models if is_openai_chat_model(m[0])]
    return ("ok", url, sorted(models, key=lambda m: m[0]))


def humanize_model_id(model_id):
    """Derive a display name from a raw model id (slug -> Title Case). Best
    effort: the API id can't recover the curated casing (e.g. "GPT-5.5"), so we
    apply a few acronym fixups and otherwise Title-Case each token."""
    words = []
    for tok in re.split(r"[/_-]", model_id):
        if not tok:
            continue
        low = tok.lower()
        if low == "gpt":
            words.append("GPT")
        elif low == "ai":
            words.append("AI")
        elif re.fullmatch(r"o\d+", low):          # o3, o4
            words.append(low)
        elif re.fullmatch(r"v\d+(\.\d+)?", low):  # v2, v2.5
            words.append(low)
        else:
            words.append(tok[:1].upper() + tok[1:])
    return " ".join(words) or model_id


def existing_context_windows(spec):
    """Map {model spec -> context_window} from the current providers.json, so a
    rewrite can keep hand-set windows for providers whose /models endpoint does
    not report one (notably OpenAI)."""
    out = {}
    for provider in spec.get("providers", []):
        for m in provider.get("models", []):
            cw = m.get("context_window")
            if isinstance(cw, int) and cw > 0:
                out[m.get("spec")] = cw
    return out


def build_model_entries(provider, models, existing_ctx):
    """Turn fetched (id, context) pairs into providers.json model objects. The
    context window falls back to the existing catalogue value when the API does
    not report one; it is omitted entirely when still unknown."""
    prefix = provider.get("model_prefix") or provider.get("id", "")
    entries = []
    for mid, ctx in models:
        spec = prefix + "/" + mid
        cw = ctx if isinstance(ctx, int) and ctx > 0 else existing_ctx.get(spec)
        entry = {"spec": spec, "display_name": humanize_model_id(mid)}
        if isinstance(cw, int) and cw > 0:
            entry["context_window"] = cw
        entries.append(entry)
    return entries


def render_models_block(entries):
    """Render a "models": [...] block as compact one-line objects matching the
    existing 2-space indentation style (entries at 8 spaces, closing ] at 6)."""
    if not entries:
        return '"models": []'
    lines = ['"models": [']
    for i, e in enumerate(entries):
        parts = [
            '"spec": %s' % json.dumps(e["spec"]),
            '"display_name": %s' % json.dumps(e["display_name"]),
        ]
        if "context_window" in e:
            parts.append('"context_window": %d' % e["context_window"])
        comma = "," if i < len(entries) - 1 else ""
        lines.append("        { %s }%s" % (", ".join(parts), comma))
    lines.append("      ]")
    return "\n".join(lines)


# A "models": [...] array as it appears in providers.json. Model entries never
# contain a literal ']', so [^\]]* safely spans exactly one array. Only provider
# objects carry a models array, so the Nth match maps to the Nth provider.
MODELS_ARRAY_RE = re.compile(r'"models"\s*:\s*\[[^\]]*\]')


def write_providers_json(raw, blocks):
    """Replace each "models" array in the raw file text with the rendered block
    at the same position, then write providers.json atomically. blocks must be
    ordered to match the providers in the file."""
    matches = MODELS_ARRAY_RE.findall(raw)
    if len(matches) != len(blocks):
        raise RuntimeError(
            "expected %d models arrays in providers.json, found %d"
            % (len(blocks), len(matches))
        )

    idx = [0]

    def repl(_m):
        block = blocks[idx[0]]
        idx[0] += 1
        return block

    new_raw = MODELS_ARRAY_RE.sub(repl, raw)

    dir_name = os.path.dirname(PROVIDERS_JSON)
    fd, tmp = tempfile.mkstemp(dir=dir_name, prefix=".providers.", suffix=".json")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(new_raw)
        os.replace(tmp, PROVIDERS_JSON)
    except BaseException:
        if os.path.exists(tmp):
            os.remove(tmp)
        raise


def main():
    with open(PROVIDERS_JSON, "r", encoding="utf-8") as fh:
        raw = fh.read()
    spec = json.loads(raw)

    env = load_env_file(ENV_FILE)
    providers = spec.get("providers", [])
    existing_ctx = existing_context_windows(spec)

    exit_code = 0
    blocks = []
    summary = []
    for provider in providers:
        pid = provider.get("id", "?")
        name = provider.get("display_name", pid)
        print("=" * 60)
        print("%s (%s)" % (name, pid))
        print("=" * 60)

        status, info, models = fetch_models(provider, env)
        if status == "ok":
            print("  endpoint: %s" % info)
            print("  %d models:" % len(models))
            for mid, ctx in models:
                ctx_str = "%s tokens" % format(ctx, ",") if ctx else "context n/a"
                print("    - %-45s %s" % (mid, ctx_str))
            entries = build_model_entries(provider, models, existing_ctx)
            blocks.append(render_models_block(entries))
            summary.append((pid, "%d models written" % len(entries)))
        else:
            if status == "skip":
                print("  skipped: %s" % info)
            else:  # error
                print("  error: %s" % info)
                exit_code = 1
            # Hard replace: a provider with no successful fetch is emptied.
            blocks.append(render_models_block([]))
            summary.append((pid, "0 — %s" % status))
        print()

    write_providers_json(raw, blocks)

    print("=" * 60)
    print("providers.json updated")
    print("=" * 60)
    for pid, note in summary:
        print("  %-14s %s" % (pid, note))

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
