# Attest onboarding: incident -> capture -> fixed-agent replay gate.
# Render from attest/assets: vhs demo.tape
# VHS encodes through YUV. Restore the exact brand background after rendering:
# python3 -c 'from pathlib import Path; p=Path("demo.gif"); b=bytearray(p.read_bytes()); n=2**((b[10]&7)+1); i=next(i for i in range(13,13+3*n,3) if b[i:i+3]==bytes((20,22,26))); b[i:i+3]=bytes((21,22,26)); p.write_bytes(b)'
# Prerequisites: brew install vhs; brew install --cask font-ibm-plex-mono
# Set up refund-desk first with its documented make setup command (Python + TS).
# REFUND_DESK may point to another prepared refund-desk checkout.
# The incident runs in a temporary copy with the repository's regression patch.
# Neither the source agent nor its committed fixtures are modified.
# Only the local scripted model server is used; no paid provider is contacted.

Output demo.gif
Require uv
Require patch
Require python3

Set Shell "bash"
Set Width 1200
Set Height 680
Set FontFamily "IBM Plex Mono, JetBrains Mono, Menlo"
Set FontSize 16
Set Padding 24
Set BorderRadius 10
Set Framerate 15
Set TypingSpeed 45ms
Set Theme {"name":"Replay","background":"#15161A","foreground":"#EDEEF0","black":"#1A1C21","red":"#FF5C5C","green":"#44D7A8","yellow":"#F2C879","blue":"#4CC9F0","magenta":"#B9A3E3","cyan":"#4CC9F0","white":"#EDEEF0","brightBlack":"#2A2D34","brightRed":"#FF5C5C","brightGreen":"#44D7A8","brightYellow":"#F2C879","brightBlue":"#4CC9F0","brightMagenta":"#B9A3E3","brightCyan":"#4CC9F0","brightWhite":"#EDEEF0","selectionBackground":"#2A2D34","cursor":"#4CC9F0"}

Hide
Type@1ms `export PS1='$ '; export DEMO_SOURCE="$(cd "${REFUND_DESK:-../../refund-desk}" && pwd)"; export DEMO_TMP="$(mktemp -d "${TMPDIR:-/tmp}/attest-demo.XXXXXX")"; trap 'rm -rf "$DEMO_TMP"' EXIT`
Enter
Wait+Line@30s /\$$/
Type@1ms `export ATTEST_ENGINE_PATH="$(env -u ATTEST_ENGINE_PATH uv run --project "$DEMO_SOURCE/py" --no-sync python -c 'from attest.engine_manager import resolve_engine; print(resolve_engine())')"`
Enter
Wait+Line@30s /\$$/
# Use private incident and fixed copies, and install the prepared dependencies
# into a private venv. No uv command can synchronize the source checkout's venv.
Type@1ms `uv run --project "$DEMO_SOURCE/py" --no-sync python -c 'import os,pathlib,shutil,subprocess; s=pathlib.Path(os.environ["DEMO_SOURCE"]); t=pathlib.Path(os.environ["DEMO_TMP"]); skip={".git",".worktrees",".stack",".attest-edge",".venv","node_modules","reports",".mypy_cache",".ruff_cache","__pycache__",".pytest_cache"}; [shutil.copytree(s,t/n,ignore=lambda p,n: skip.intersection(n)) for n in ("incident","fixed")]; d=t/"incident"; [(t/n/"ts/node_modules").symlink_to(s/"ts/node_modules",target_is_directory=True) for n in ("incident","fixed")]; subprocess.run(["patch","--batch","--forward","--fuzz=0","-p1","-i",str(s/"regression/revert-inc-1042-fix.patch")],cwd=d,check=True); (d/".channel").write_text("edge\n"); (d/"py/tests/attest/fixtures/inc-1042.attest.json").unlink(); subprocess.run(["uv","venv","--python",str(s/"py/.venv/bin/python"),str(t/"venv")],check=True); req=subprocess.check_output(["uv","pip","freeze","--python",str(s/"py/.venv/bin/python")],text=True).replace(str(s/"py"),str(d/"py")); subprocess.run(["uv","pip","install","--python",str(t/"venv/bin/python"),"-r","-"],input=req,text=True,check=True); [(t/n/"py/.venv").symlink_to(t/"venv",target_is_directory=True) for n in ("incident","fixed")]; print("DEMO_COPY_READY")'`
Enter
Wait+Line@30s /\$$/
# A private port avoids interfering with a developer's usual demo stack.
# Only synthetic credentials are supplied to the local scripted endpoint; the planted
# demo key is read from refund-desk's stack.mk rather than duplicated here.
Type@1ms `export MODEL_PORT=18797 OPENAI_BASE_URL=http://127.0.0.1:18797/v1 MODEL_NAME=refund-desk-scripted OPENAI_API_KEY="$(sed -n 's/^OPENAI_API_KEY ?= //p' "$DEMO_SOURCE/stack.mk")" PAYMENTS_TIMEOUT_MS=1200; unset ANTHROPIC_API_KEY GOOGLE_API_KEY GEMINI_API_KEY UV_NO_SYNC; export PYTHONPATH="$DEMO_TMP/incident/py/src" UV_PROJECT="$DEMO_TMP/incident/py" UV_PROJECT_ENVIRONMENT="$DEMO_TMP/venv" MAKEFLAGS=s CHANNEL=edge; cd "$DEMO_TMP/incident"; clear`
Enter
Sleep 500ms
Show

Type "# Test AI agents like you test your code"
Enter
Sleep 2.5s
Type "# INC-1042: the reply is right. The ledger is not."
Enter
Type "make incident"
Enter
Wait+Line@20s /\$$/
Sleep 3.5s

Hide
Ctrl+L
# make incident owns its model server. Start a fresh private stack for capture
# only after it exits; UV_NO_SYNC preserves the private, prepared edge packages.
Type@1ms `export UV_NO_SYNC=1; uv run python scripts/stack.py -- bash --noprofile --norc`
Enter
Wait+Line@30s /\$$/
Type@1ms "clear"
Enter
Sleep 250ms
Show
Type "# Capture the incident. Draft and verify its contracts."
Enter
Type "uv run attest capture --name inc-1042 --expect-fail "
Type "--world world/payments-inc-1042.world.yaml -- "
Type "python scripts/incident.py --lang py --in-world"
Enter
Wait+Line@20s /\$$/
Sleep 4s

# Replay the fixed copy's committed fixture without touching the source checkout.
Hide
Type@1ms `cd "$DEMO_TMP/fixed/py"; export PYTHONPATH="$DEMO_TMP/fixed/py/src" UV_PROJECT="$DEMO_TMP/fixed/py" OPENAI_API_KEY=replay-needs-no-key; clear`
Enter
Sleep 250ms
Show
Type "# After the fix: strict replay becomes the merge gate."
Enter
Type "uv run pytest tests/replay --attest-replay=strict --attest-egress=block -q"
Enter
Wait+Line@20s /\$$/
Sleep 4s
Type "# Real agent code. Recorded responses. Zero model calls."
Enter
Sleep 3s

Hide
# Exit the stack's child shell, then its parent; the stack stops its server and
# the EXIT trap removes only this recording's temporary directory.
Type@1ms "exit"
Enter
Sleep 1s
Type@1ms "exit"
Enter
Sleep 500ms
