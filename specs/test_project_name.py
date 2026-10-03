"""Project name resolution, like docker-compose: flag > env > `name:` > directory name."""

import pytest

from support import spec


def test_the_name_defaults_to_the_directory_name(project):
    assert project.resolved(spec())["name"] == project.name


def test_name_in_the_file_overrides_the_directory(project):
    assert project.resolved(spec(name="from-file"))["name"] == "from-file"


def test_the_environment_overrides_the_file(project):
    result = project.config(spec(name="from-file"), env={"EGZO_PROJECT_NAME": "from-env"})
    assert result.yaml()["name"] == "from-env"


def test_the_flag_overrides_the_environment(project):
    project.write(spec(name="from-file"))
    result = project.run("-p", "from-flag", "config", env={"EGZO_PROJECT_NAME": "from-env"})
    assert result.yaml()["name"] == "from-flag"


def test_the_long_flag_is_equivalent(project):
    project.write(spec())
    result = project.run("--project-name", "from-flag", "config")
    assert result.yaml()["name"] == "from-flag"


def test_names_are_normalized_to_lowercase(project):
    assert project.resolved(spec(name="MyProj"))["name"] == "myproj"


@pytest.mark.parametrize("bad", ["-leading-dash", "_leading-underscore", "has space", "dots.not.allowed", ""])
def test_invalid_names_are_rejected(project, bad):
    result = project.config(spec(name=bad))
    assert result.returncode != 0
    assert "name" in result.stderr


@pytest.mark.todo
def test_a_name_in_use_from_another_directory_refuses_every_command(live_project, make_project):
    assert live_project.run("up").returncode == 0
    live_project.write(spec())

    other = make_project(env=live_project.env)
    other.write(spec())
    for command in (["ps"], ["up"], ["down"]):
        result = other.run("-p", live_project.name, *command)
        assert result.returncode != 0, command
        assert str(live_project.root) in result.stderr, command
