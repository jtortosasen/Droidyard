// The syntax colours (plan m2i D12): one HighlightStyle whose every colour is a `--syn-*`
// custom property of ui/tokens.css (dark and light there), so a scheme switch needs no code
// and this file holds no colour literal.
import { HighlightStyle } from '@codemirror/language';
import { tags as t } from '@lezer/highlight';

export const highlightStyle = HighlightStyle.define([
	{ tag: [t.keyword, t.modifier, t.operatorKeyword, t.controlKeyword, t.definitionKeyword], color: 'var(--syn-keyword)' },
	{ tag: [t.string, t.special(t.string), t.regexp], color: 'var(--syn-string)' },
	{ tag: [t.comment], color: 'var(--syn-comment)', fontStyle: 'italic' },
	{ tag: [t.number, t.bool, t.null, t.atom], color: 'var(--syn-number)' },
	{ tag: [t.typeName, t.className, t.namespace], color: 'var(--syn-type)' },
	{ tag: [t.function(t.variableName), t.function(t.propertyName), t.labelName], color: 'var(--syn-function)' },
	{ tag: [t.propertyName, t.definition(t.propertyName), t.definition(t.variableName)], color: 'var(--syn-property)' },
	{ tag: [t.tagName, t.angleBracket], color: 'var(--syn-tag)' },
	{ tag: [t.attributeName], color: 'var(--syn-attribute)' },
	{ tag: [t.heading], color: 'var(--syn-heading)', fontWeight: 'bold' },
	{ tag: [t.link, t.url], color: 'var(--syn-link)' },
	{ tag: [t.meta, t.annotation, t.processingInstruction, t.documentMeta], color: 'var(--syn-meta)' },
	{ tag: [t.strong], fontWeight: 'bold' },
	{ tag: [t.emphasis], fontStyle: 'italic' },
]);
