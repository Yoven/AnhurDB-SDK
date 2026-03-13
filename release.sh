#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1

gh alias set rc 'release create'
gh release create v2.0.0 --title "AnhurDB SDK V2" --generate-notes
