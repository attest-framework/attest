### attest: 0 passed · 1 failed · 1 not evaluated

0 replayed · 0 live model calls · $0.00

**Not evaluated (assertions):** `@octocat<img src=x>` ×1

**Failures and replay misses**

| Location | Test | Assertion | Explanation |
| --- | --- | --- | --- |
| `@org/team/org/repo#1.py:3` | ``tests/@octocat.py::test_`x`\|y`` | `effect` | ```@octocat @org/team org/repo#1 <img src=x onerror=alert(1)> <!-- [x](https://evil) <!-- attest:pr-annotate --> `tick` ``double`` \| pipe bidizw``` |

**Policy:** fail (exit 1) · regression verdict `@org/team`
- `block@x` ``rule`with`ticks``: `<!-- attest:pr-annotate --> @octocat [x](https://evil) <img src=x> unclosed <!--`
