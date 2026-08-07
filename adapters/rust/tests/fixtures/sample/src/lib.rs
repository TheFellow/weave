pub trait Greeter {
    fn greet(&self, name: &str) -> String;
}

pub struct ConsoleGreeter;

impl Greeter for ConsoleGreeter {
    fn greet(&self, name: &str) -> String {
        format!("hello {name}")
    }
}

pub fn greet(name: &str) -> String {
    ConsoleGreeter.greet(name)
}

#[cfg(test)]
mod tests {
    use super::greet;

    #[test]
    fn greets_a_name() {
        assert_eq!(greet("weave"), "hello weave");
    }
}
