### attest: 1 passed · 2 failed · 2 not evaluated

4 replayed · 1 live model calls · $0.04 · 2 replay misses

3 model calls · 1500 tokens (100 cached) · $0.0123 · retry cost $0.0010

**Not evaluated (assertions):** `provider_not_configured` ×1 · `replay_miss` ×1

**Failures and replay misses**

| Location | Test | Assertion | Explanation |
| --- | --- | --- | --- |
| `tests/test_refund.py:40` | `tests/test_refund.py::test_live_agent` | `effect` | `effect violated: 2 refunds for O-1042 (expected 1)` |
| `tests/test_replay.py:12` | `tests/test_replay.py::test_auto` | `replay (replay_miss)` | `replay miss: system prompt changed (hash a1b2c3d→e4f5a6b) \| re-record` |
| `tests/test_replay.py:30` | `tests/test_replay.py::test_strict` | `replay (replay_miss)` | `replay miss: tools changed (hash 1111111→2222222)` |

**Policy:** fail (exit 1)
- `block` `hard_fail`: `1 hard failure`
