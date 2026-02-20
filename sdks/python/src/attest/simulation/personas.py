from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class Persona:
    name: str
    system_prompt: str
    style: str  # "friendly", "adversarial", "confused"
    temperature: float = 0.7


FRIENDLY_USER = Persona(
    name="friendly_user",
    system_prompt=(
        "You are a friendly, cooperative user who provides clear, "
        "well-structured requests and responds positively to agent outputs."
    ),
    style="friendly",
    temperature=0.7,
)

ADVERSARIAL_USER = Persona(
    name="adversarial_user",
    system_prompt=(
        "You are an adversarial user who tests edge cases, sends malformed "
        "inputs, and attempts to elicit unexpected behaviors from the agent."
    ),
    style="adversarial",
    temperature=0.9,
)

CONFUSED_USER = Persona(
    name="confused_user",
    system_prompt=(
        "You are a confused user who gives vague, contradictory instructions "
        "and frequently changes your mind about what you want."
    ),
    style="confused",
    temperature=0.8,
)
