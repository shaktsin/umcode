# UMCode Skill Template

This is a template for creating UMCode skills. It demonstrates both Python and Bash scripts with proper input/output handling.

## Quick Start

### 1. Clone this template

```bash
git clone https://github.com/yourusername/umcode-skill-example.git my-skill
cd my-skill
```

### 2. Customize your skill

- Edit `SKILL.md` to define your skill metadata and scripts
- Add your scripts to the `scripts/` directory
- Update `requirements.txt` with Python dependencies
- Update `pyproject.toml` with your package information

### 3. Test locally

```bash
# Install in development mode
umcode skill install .

# Confirm it is loaded
umcode skill list
# Then start a chat and trigger the skill
```

## Skill Structure

```
my-skill/
├── SKILL.md              # Skill manifest (required)
├── README.md            # Documentation
├── requirements.txt     # Python dependencies
├── pyproject.toml       # Optional package metadata
└── scripts/            # Executable scripts
    ├── hello.py        # Python script example
    ├── hello.sh        # Bash script example
    └── fetch_data.py   # Python script with dependencies
```

## SKILL.md Format

The `SKILL.md` file uses YAML frontmatter to define skill metadata:

```yaml
---
name: my_skill               # Unique skill identifier
version: 1.0.0              # Semantic version
description: Short description
allowed_tools: []           # Tools this skill can use
risk_level: green          # green, yellow, or red
triggers:                   # Keywords that activate this skill
  - "my skill"

scripts:                    # Executable scripts
  my_script:
    path: scripts/my_script.py
    description: What this script does
    input_schema:           # JSON Schema for input validation
      type: object
      properties:
        param1:
          type: string
          description: Parameter description
      required:
        - param1
    examples:               # Example inputs for the LLM
      - param1: "value"

runtime:
  timeout_seconds: 30      # Maximum execution time
---

# Skill Documentation

Markdown documentation goes here...
```

## Script Input/Output

### Input Format (stdin)

Scripts receive JSON via stdin:

```json
{
  "input": {
    "param1": "value1",
    "param2": "value2"
  },
  "config": {
    "api_key": "from-config.yaml",
    "other_setting": "value"
  }
}
```

### Output Format (stdout)

Scripts should output JSON:

```json
{
  "message": "Human-readable result",
  "data": {
    "key": "value",
    "structured": "data"
  }
}
```

## Python Script Template

```python
#!/usr/bin/env python3
import json
import sys

def main():
    # Read input
    data = json.load(sys.stdin)
    input_params = data.get("input", {})
    config = data.get("config", {})

    # Your logic here
    result = process(input_params, config)

    # Output result
    print(json.dumps({
        "message": "Success",
        "data": result
    }))

if __name__ == "__main__":
    main()
```

## Bash Script Template

```bash
#!/bin/bash
# Read JSON from stdin
INPUT=$(cat)

# Parse with jq or python
NAME=$(echo "$INPUT" | jq -r '.input.name // "default"')

# Your logic here

# Output JSON
cat <<EOF
{
  "message": "Success",
  "data": {
    "result": "$NAME"
  }
}
EOF
```

## Configuration

Users can configure your skill in their `config.yaml`:

```yaml
skill_configs:
  my_skill:
    args:
      api_key: "sk-..."
      base_url: "https://api.example.com"
    env:
      MY_SKILL_TOKEN: "token123"
```

Access these in your scripts:
- **Python**: `config = data.get("config", {})`
- **Bash**: Parse from `$INPUT` JSON

## Best Practices

1. **Validate inputs**: Use JSON Schema in SKILL.md
2. **Handle errors**: Return error messages in JSON
3. **Timeout aware**: Keep scripts under timeout_seconds
4. **Security**: Never execute arbitrary code from inputs
5. **Documentation**: Write clear descriptions and examples
6. **Testing**: Test with various inputs before publishing

## Publishing Checklist

- [ ] Unique skill name
- [ ] All scripts are executable (`chmod +x scripts/*.sh`)
- [ ] requirements.txt includes all dependencies
- [ ] pyproject.toml has correct metadata
- [ ] README.md is complete
- [ ] Tested locally with `umcode skill install .`
- [ ] Version follows semver
- [ ] License specified (MIT, Apache, etc.)

## License

MIT - See LICENSE file
