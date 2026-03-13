#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1
git add v2/golang/models/record.go
git add v2/python/anhurdb/crypto/quantizer.py
git add v2/python/anhurdb/client/connection.py
git add v2/python/anhurdb/client/__init__.py
git add v2/python/anhurdb/query/builder.py
git add .github/workflows/ci.yml
git commit -m "fix: resolve python types, go unreferenced models, and split CI jobs"
git push
