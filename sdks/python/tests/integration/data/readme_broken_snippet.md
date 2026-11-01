# Deliberately broken README

The README snippet test must fail on this file: the snippet calls a method the SDK does not have.

```python
from attest import Trace, expect
from attest.plugin import AttestEngineFixture


def test_broken(attest: AttestEngineFixture, my_agent_result: Trace) -> None:
    attest.evaluate(expect(my_agent_result).output_containz("refund"))
```
