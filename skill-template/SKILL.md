---
name: example
version: 1.0.0
description: Example skill demonstrating Python and Bash scripts
allowed_tools: []
risk_level: green
triggers:
  - "example skill"

scripts:
  hello_python:
    path: scripts/hello.py
    description: Python script that greets the user
    input_schema:
      type: object
      properties:
        name:
          type: string
          description: Name to greet
      required:
        - name
      additionalProperties: false
    examples:
      - name: "World"

  hello_bash:
    path: scripts/hello.sh
    description: Bash script that greets the user
    input_schema:
      type: object
      properties:
        name:
          type: string
          description: Name to greet
      required:
        - name
      additionalProperties: false
    examples:
      - name: "World"

  fetch_data:
    path: scripts/fetch_data.py
    description: Python script demonstrating HTTP requests
    input_schema:
      type: object
      properties:
        url:
          type: string
          description: URL to fetch
      required:
        - url
      additionalProperties: false

runtime:
  timeout_seconds: 30
---

# Example Skill

This is an example skill template for UMCode. It demonstrates:

- Python scripts
- Bash scripts
- Input validation with JSON schemas
- Reading from stdin
- Accessing skill configuration

## Installation

```bash
# Install from local path
umcode skill install /path/to/this/skill

# Or from GitHub
umcode skill install https://github.com/yourusername/umcode-skill-example.git
```

## Scripts

### hello_python
Python script that greets the user by name.

### hello_bash
Bash script that greets the user by name.

### fetch_data
Python script that fetches data from a URL using requests library.
