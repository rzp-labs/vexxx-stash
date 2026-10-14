#!/usr/bin/env bash
# Run the Python regression suites under coverage.py and write Cobertura XML
# for Sonar. Run from the repository root.
set -euo pipefail

venv="${RUNNER_TEMP:-/tmp}/python-coverage"
# System site packages keep the runner's PyYAML available to the tests.
python3 -m venv --system-site-packages "$venv"
"$venv/bin/pip" install --quiet --require-hashes -r scripts/coverage-requirements.txt

mkdir -p dist/coverage
run=("$venv/bin/python" -m coverage run --rcfile=scripts/coverage.ini --append -m unittest discover)
rm -f dist/coverage/.python-coverage
"${run[@]}" -s scripts -p 'test_*.py'
if test -d scripts/gpu_generation; then
  "${run[@]}" -s scripts/gpu_generation -p 'test_*.py'
fi
"$venv/bin/python" -m coverage xml --rcfile=scripts/coverage.ini -o dist/coverage/python.xml
test -s dist/coverage/python.xml
