#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1

git add v2/python/pyproject.toml v2/python/README.md
git commit -m "fix: resolve poetry build missing parent relative readme during ci"
git push
