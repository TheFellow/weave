import sys

from .adapter import AdapterError, PROTOCOL, describe, index


def _utf8_stdio():
    for stream in (sys.stdin, sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if reconfigure is not None:
            reconfigure(encoding="utf-8")


def main():
    _utf8_stdio()
    try:
        if sys.argv[1:] == ["describe", "--protocol", PROTOCOL]:
            describe(sys.stdout)
            return 0
        if sys.argv[1:] == ["index", "--protocol", PROTOCOL]:
            index(sys.stdin, sys.stdout)
            return 0
        raise AdapterError(
            "usage: weave-python (describe|index) --protocol weave.adapter/v0"
        )
    except (AdapterError, OSError, SyntaxError, UnicodeError, ValueError) as error:
        print("weave-python: {}".format(error), file=sys.stderr)
        return 1
    except BrokenPipeError:
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
