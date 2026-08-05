You have just produced an implementation plan. Generate a visual whiteboard representation of it so the user can walk through the changes before execution.

## The plan

$(plan)

---

## Your task

Produce a JSON array of **scenes** that visualises this plan. Output **only** the raw JSON array — no preamble, no explanation, no markdown fences.

Each scene contains a **Mermaid flowchart**. You do not place nodes by hand — just write the flowchart and the harness lays it out.

## Scene grouping

Do **not** create one scene per step. Group related steps into scenes that tell a coherent story:

- Steps that touch the same layer (UI, API, database, config) belong together on one scene
- Steps with a tight dependency chain work well as a single flow diagram
- A typical plan needs **2–3 scenes**, not one per step

Good scene names: `"Data Model"`, `"API Layer"`, `"Frontend Changes"`, `"Auth Flow"`, `"Before / After"`.

---

## Scenes JSON Schema

The top level is an **array of scene objects**:

```json
[
  {
    "name": "Architecture",
    "context": "One to three sentences describing what this scene shows and how it relates to the plan. Read aloud by the voice agent.",
    "mermaid": "graph LR\n  A[Client] --> B[API]\n  B --> C[(Database)]"
  }
]
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Display name shown in the scene list. |
| `context` | string | yes | 1–3 sentences explaining the scene (used by the voice agent). |
| `mermaid` | string | yes | A Mermaid **flowchart** for this scene. Newlines as `\n`. |

## Writing the Mermaid flowchart

- Start with a direction: `graph LR` (left→right) or `graph TD` (top→down).
- Node shapes map to the canvas:
  - `A[Label]` → rectangle (services, components, files)
  - `B{Label}` → diamond (decisions, branches)
  - `C[(Label)]` → database (datastores, queues)
- Edges: `A --> B`, with optional labels `A -->|creates| B` or `A -- reads --> B`.
- Keep node labels short (1–3 words). Use edge labels to explain relationships.
- Use only flowchart syntax (`graph`/`flowchart`). Do not use other Mermaid diagram types here.

---

## Example

```json
[
  {
    "name": "Architecture",
    "context": "The request path: the gateway authenticates, then reads and writes the store.",
    "mermaid": "graph LR\n  A[API Gateway] -->|authenticated| B{Authorized?}\n  B -->|yes| C[Service]\n  B -->|no| D[Reject]\n  C --> E[(PostgreSQL)]"
  },
  {
    "name": "Auth Flow",
    "context": "How a client obtains and uses a token.",
    "mermaid": "graph TD\n  A[Client] --> B[Login]\n  B --> C{Valid?}\n  C -->|yes| D[Issue token]\n  C -->|no| E[401]"
  }
]
```
