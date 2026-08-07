import os

DEFAULT = "world"
DEFAULT = "friend"
π = 3
source_values = [1]
copied_values = [source_values for source_values in source_values]
first_lambda = lambda item: item; second_lambda = lambda item: item


def normalize(value):
    return value.strip()


def greet(name=DEFAULT):
    result = normalize(name)
    return result


class Greeter:
    def greet(self, name):
        return greet(name)


def current_directory():
    return os.getcwd()


def outer():
    value = "outer"

    def inner():
        nonlocal value
        return value

    return inner
