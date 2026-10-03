"""Small builders so specs read as the YAML they describe."""

VAULT = {
    "main": {
        "backend": "env",
        "secrets": {
            "ANTHROPIC_API_KEY": {"from": "env:ANTHROPIC_API_KEY"},
            "GITHUB_TOKEN": {"from": "env:GITHUB_TOKEN"},
            "DEPLOY_TOKEN": {"from": "env:DEPLOY_TOKEN"},
        },
    }
}

GIT_WORKSPACE = {"repo": {"git": {"url": "https://github.com/acme/shop.git"}}}


def spec(**sections):
    """An egzo.yaml as a dict, with the shared vault unless overridden."""
    document = {"vaults": VAULT}
    document.update(sections)
    return document


def agent(**fields):
    """An agent definition; claude-code unless overridden."""
    definition = {"harness": "claude-code"}
    definition.update(fields)
    return definition


def anthropic_profile(**extra):
    """The profile that lets an agent reach its model API."""
    profile = {
        "allow": ["platform.claude.com"],
        "services": {"anthropic": "main/ANTHROPIC_API_KEY"},
    }
    profile.update(extra)
    return profile


def mounts(resolved, agent_name):
    """The resolved workspaces of an agent, keyed by mount path."""
    return {m["mount"]: m for m in resolved["agents"][agent_name]["workspaces"]}


def profile(resolved, name):
    return resolved["egress"][name]
