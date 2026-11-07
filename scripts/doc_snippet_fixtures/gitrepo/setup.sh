#!/bin/sh
# A local repository with an origin/main ref (no remote), so `attest select --base origin/main` has a base to diff against.
set -e
git init -q .
git -c user.name=docs -c user.email=docs@example.invalid add tests
git -c user.name=docs -c user.email=docs@example.invalid commit -q -m base
git update-ref refs/remotes/origin/main HEAD
