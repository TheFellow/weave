package example

class FriendlyGreeter : Greeter {
    override fun greet(name: String): String = "Hello, $name!"
}
