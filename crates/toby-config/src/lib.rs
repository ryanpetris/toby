//! Configuration and manifest parsing, path resolution and secret substitution.

pub mod global;
pub mod machine;
pub mod paths;
pub mod subst;

/// A new unique ID (ULID) for machines, sessions and other objects.
pub fn new_id() -> String {
    ulid::Ulid::generate().to_string()
}
