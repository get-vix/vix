# Prompt Loader Package

This package provides a simple template engine for loading and processing prompt templates with variable substitution.

## Features

- **Template Loading**: Load markdown prompt templates from disk
- **Variable Substitution**: Replace `$(name)` placeholders with runtime values
- **File Inclusion**: Include brain context files via `$(file:path)` placeholders
- **Function Calls**: Execute registered functions via `$(call:name)` placeholders
- **Caching**: Automatically cache loaded templates for performance
- **Graceful Degradation**: Return placeholders if files/variables are missing

## Usage

### Basic Loading

```go
import "github.com/get-vix/vix/internal/daemon/prompt"

loader := prompt.GetLoader()
content := loader.Load("prompt/chat/system.md", nil, "")
```

### Variable Substitution

```go
vars := map[string]string{
    "working_directory": "/home/user/project",
    "model": "claude-3-5-sonnet-20241022",
}

content := loader.Load("prompt/chat/system.md", vars, "")
```

Template file (`prompt/chat/system.md`):
```markdown
Working directory: $(working_directory)
Model: $(model)
```

Result:
```
Working directory: /home/user/project
Model: claude-3-5-sonnet-20241022
```

### File Inclusion

```go
brainDir := ".vix"
content := loader.Load("prompt/chat/system.md", nil, brainDir)
```

Template file:
```markdown
# System Prompt

$(file:context/architecture.md)

$(file:context/project-summary.md)
```

Result includes the content of both brain context files.

### Combined Example

```go
vars := map[string]string{
    "working_directory": "/home/user/project",
}
brainDir := ".vix"

loader := prompt.GetLoader()
content := loader.Load("prompt/chat/system.md", vars, brainDir)
```

Template:
```markdown
You are a helpful AI assistant.

Working directory: $(working_directory)

# Project Knowledge

$(file:context/project-summary.md)
```

## Placeholder Syntax

- `$(name)` - Replaced with the value from the vars map
- `$(file:path)` - Replaced with the content of the file at `brainDir/path`
- `$(call:name)` - Calls the registered function `name` and replaced with its return value

## Error Handling

The loader uses graceful degradation:

- If a template file is not found, returns an error message
- If a referenced file is missing, keeps the `$(file:...)` placeholder
- If a variable is missing, keeps the `$(name)` placeholder

This ensures prompts remain functional even with partial context.

## Cache Management

Templates are cached after first load. To force a reload (e.g., during development):

```go
loader.ClearCache()
```

## Singleton Pattern

The package provides a global singleton loader via `GetLoader()`:

```go
loader1 := prompt.GetLoader()
loader2 := prompt.GetLoader()
// loader1 == loader2 (same instance)
```
