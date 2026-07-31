use ahash::AHashMap;
use pig_tokenizer_native::{BlockDigestContext, NativeTokenizer};
use tokenizers::{Tokenizer, models::wordlevel::WordLevel, pre_tokenizers::whitespace::Whitespace};

#[test]
fn analysis_returns_count_chained_full_blocks_and_partial_metadata_without_ids() {
    let native = word_tokenizer();
    let context = digest_context("profile-1", "backend-1", 4);

    let analysis = native
        .analyze("a b c d e f g h i j", false, &context, false)
        .expect("analyze");

    assert_eq!(analysis.token_count, 10);
    assert_eq!(analysis.full_block_digests.len(), 2);
    assert!(
        analysis
            .full_block_digests
            .iter()
            .all(|digest| *digest != [0; 32])
    );
    let partial = analysis.partial_block.expect("partial block");
    assert_eq!(partial.token_count, 2);
    assert_ne!(partial.digest, [0; 32]);
    assert!(analysis.token_ids.is_none());
    assert_ne!(analysis.input_sha256, [0; 32]);
}

#[test]
fn chained_digests_preserve_only_the_prefix_before_a_changed_token() {
    let native = word_tokenizer();
    let context = digest_context("profile-1", "backend-1", 4);
    let base = native
        .analyze("a b c d e f g h", false, &context, false)
        .expect("base");
    let first_changed = native
        .analyze("a b x d e f g h", false, &context, false)
        .expect("first changed");
    let second_changed = native
        .analyze("a b c d e f x h", false, &context, false)
        .expect("second changed");

    assert_ne!(
        base.full_block_digests[0],
        first_changed.full_block_digests[0]
    );
    assert_ne!(
        base.full_block_digests[1],
        first_changed.full_block_digests[1]
    );
    assert_eq!(
        base.full_block_digests[0],
        second_changed.full_block_digests[0]
    );
    assert_ne!(
        base.full_block_digests[1],
        second_changed.full_block_digests[1]
    );
}

#[test]
fn digest_identity_and_optional_token_ids_are_explicit() {
    let native = word_tokenizer();
    let first = native
        .analyze(
            "a b c d",
            false,
            &digest_context("profile-1", "backend-1", 4),
            true,
        )
        .expect("first");
    let different_manifest = native
        .analyze(
            "a b c d",
            false,
            &digest_context("profile-2", "backend-1", 4),
            false,
        )
        .expect("different manifest");
    let different_epoch = native
        .analyze(
            "a b c d",
            false,
            &digest_context("profile-1", "backend-2", 4),
            false,
        )
        .expect("different epoch");

    assert_eq!(first.token_ids, Some(vec![1, 2, 3, 4]));
    assert_ne!(
        first.full_block_digests,
        different_manifest.full_block_digests
    );
    assert_ne!(first.full_block_digests, different_epoch.full_block_digests);
}

#[test]
fn invalid_digest_context_is_rejected_before_encoding() {
    assert!(BlockDigestContext::new("", "backend-1", 4, &[7; 32]).is_err());
    assert!(BlockDigestContext::new("profile-1", "", 4, &[7; 32]).is_err());
    assert!(BlockDigestContext::new("profile-1", "backend-1", 0, &[7; 32]).is_err());
    assert!(BlockDigestContext::new("profile-1", "backend-1", 4, &[7; 15]).is_err());
}

fn digest_context(manifest_id: &str, backend_epoch: &str, block_size: usize) -> BlockDigestContext {
    BlockDigestContext::new(manifest_id, backend_epoch, block_size, &[7; 32])
        .expect("digest context")
}

fn word_tokenizer() -> NativeTokenizer {
    let vocabulary = AHashMap::from([
        ("[UNK]".to_string(), 0),
        ("a".to_string(), 1),
        ("b".to_string(), 2),
        ("c".to_string(), 3),
        ("d".to_string(), 4),
        ("e".to_string(), 5),
        ("f".to_string(), 6),
        ("g".to_string(), 7),
        ("h".to_string(), 8),
        ("i".to_string(), 9),
        ("j".to_string(), 10),
        ("x".to_string(), 11),
    ]);
    let model = WordLevel::builder()
        .vocab(vocabulary)
        .unk_token("[UNK]".to_string())
        .build()
        .expect("word-level model");
    let mut tokenizer = Tokenizer::new(model);
    tokenizer.with_pre_tokenizer(Some(Whitespace));
    NativeTokenizer::from_tokenizer(tokenizer)
}
