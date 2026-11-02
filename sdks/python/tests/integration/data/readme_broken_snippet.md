# Deliberately broken README

The README snippet test must fail on this file: the snippet calls a method the SDK does not have.

```python
from attest import TraceBuilder, expect
from attest.plugin import AttestEngineFixture


def test_broken(attest: AttestEngineFixture) -> None:
    trace = TraceBuilder(agent_id="a").set_output(message="Your refund").build()
    attest.evaluate(expect(trace).output_containz("refund"))
```
