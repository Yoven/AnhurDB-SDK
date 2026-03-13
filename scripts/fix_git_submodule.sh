#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
export PYTHONUNBUFFERED=1

git rm --cached v2/golang
rm -rf v2/golang/.git
git add v2/golang
git commit -m "fix: remove embedded v2/golang repository to allow github checkout"
git push
