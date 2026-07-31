#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use tokenizers::{Tokenizer, models::wordlevel::WordLevel, pre_tokenizers::whitespace::Whitespace};

    use super::NativeTokenizer;

    #[test]
    fn returns_exact_ids_from_the_loaded_tokenizer() {
        let vocabulary = HashMap::from([
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
