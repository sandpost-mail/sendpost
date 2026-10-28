import os
import uuid

import pytest


@pytest.fixture
def run_id():
    """One run id per test: the base is the CI run (SANDPOST_RUN_ID), the suffix
    keeps tests of the same job from reading each other's mail."""
    base = os.environ.get("SANDPOST_RUN_ID", "local")
    return f"{base}-{uuid.uuid4().hex[:8]}"
