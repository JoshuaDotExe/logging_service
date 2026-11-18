#!/usr/bin/env python3
"""Send a small, rate-limited batch of log-ingestion requests to localhost."""

import argparse
import os
import sys
import time
from collections import Counter
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


MAX_REQUESTS = 1000000
MIN_INTERVAL_SECONDS = 0.00000001
ENDPOINT = os.environ.get("LOGSERVICE_URL", "https://localhost/logs")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--requests",
        type=int,
        default=10,
        help=f"number of requests (1-{MAX_REQUESTS}; default: 10)",
    )
    parser.add_argument(
        "--interval",
        type=float,
        default=1.0,
        help=f"seconds between requests (minimum: {MIN_INTERVAL_SECONDS})",
    )
    args = parser.parse_args()

    api_key = os.environ.get("LOG_API_KEY")
    if not api_key:
        parser.error("set LOG_API_KEY in the environment")
    if not 1 <= args.requests <= MAX_REQUESTS:
        parser.error(f"--requests must be between 1 and {MAX_REQUESTS}")
    if args.interval < MIN_INTERVAL_SECONDS:
        parser.error(f"--interval must be at least {MIN_INTERVAL_SECONDS} seconds")

    statuses = Counter()
    for index in range(args.requests):
        body = f'{{"message":"local load test {index + 1}"}}'.encode()
        request = Request(
            ENDPOINT,
            data=body,
            headers={
                "Content-Type": "application/json",
                "X-API-Key": api_key,
            },
            method="POST",
        )
        try:
            with urlopen(request, timeout=10) as response:
                status = response.status
        except HTTPError as error:
            status = error.code
        except (URLError, TimeoutError, OSError) as error:
            print(f"Request {index + 1}: connection error: {error}", file=sys.stderr)
            statuses["connection_error"] += 1
        else:
            print(f"Request {index + 1}: HTTP {status}")
            statuses[str(status)] += 1

        if index + 1 < args.requests:
            time.sleep(args.interval)

    print(f"Completed {args.requests} requests: {dict(statuses)}")
    return 0 if statuses == Counter({"201": args.requests}) else 1


if __name__ == "__main__":
    raise SystemExit(main())
