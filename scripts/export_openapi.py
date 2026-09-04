"""Print a stable OpenAPI document without connecting to the database."""

import json

from circular.api.main import app

print(json.dumps(app.openapi(), indent=2, sort_keys=True))
