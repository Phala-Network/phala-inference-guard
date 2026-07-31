use std::{error::Error, fmt, path::Path};

use tokenizers::Tokenizer;

#[derive(Debug)]
pub struct NativeTokenizer {
    tokenizer: Tokenizer,
}

#[derive(Debug)]
pub struct NativeTokenizerError {
    message: String,
}

impl NativeTokenizer {
    pub fn from_file(path: impl AsRef<Path>) -> Result<Self, NativeTokenizerError> {
        let tokenizer = Tokenizer::from_file(path.as_ref()).map_err(|error| {
            NativeTokenizerError::new(format!("load tokenizer: {error}"))
        })?;
        Ok(Self::from_tokenizer(tokenizer))
    }

    pub fn from_tokenizer(tokenizer: Tokenizer) -> Self {
        Self { tokenizer }
    }

    pub fn encode(
        &self,
        input: &str,
        add_special_tokens: bool,
    ) -> Result<Vec<u32>, NativeTokenizerError> {
        let encoding = self
            .tokenizer
            .encode(input, add_special_tokens)
            .map_err(|error| NativeTokenizerError::new(format!("encode input: {error}")))?;
        Ok(encoding.get_ids().to_vec())
    }
}

impl NativeTokenizerError {
    fn new(message: String) -> Self {
        Self { message }
    }
}

impl fmt::Display for NativeTokenizerError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl Error for NativeTokenizerError {}

#[cfg(test)]
mod tests {
    use ahash::AHashMap;
    use tokenizers::{
        Tokenizer, models::wordlevel::WordLevel, pre_tokenizers::whitespace::Whitespace,
    };

    use super::NativeTokenizer;

    #[test]
    fn returns_exact_ids_from_the_loaded_tokenizer() {
        let vocabulary = AHashMap::from([
            ("[UNK]".to_string(), 0),
            ("hello".to_string(), 1),
            ("world".to_string(), 2),
        ]);
        let model = WordLevel::builder()
            .vocab(vocabulary)
            .unk_token("[UNK]".to_string())
            .build()
            .expect("word-level model");
        let mut tokenizer = Tokenizer::new(model);
        tokenizer.with_pre_tokenizer(Some(Whitespace));

        let native = NativeTokenizer::from_tokenizer(tokenizer);
        let token_ids = native.encode("hello world", false).expect("encode");

        assert_eq!(token_ids, vec![1, 2]);
    }

    #[test]
    fn invalid_tokenizer_file_is_rejected() {
        let error = NativeTokenizer::from_file("/does/not/exist/tokenizer.json")
            .expect_err("missing tokenizer must fail");
        assert!(error.to_string().contains("load tokenizer"));
    }
}
