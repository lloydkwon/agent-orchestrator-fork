// LADO fork 기본값 (docs/LJY-PRD/TDD.md §11): 웹 링크를 그냥 클릭하면 시스템
// 기본 브라우저(사용자의 Chrome)로 연다. 내장 Browser 패널은 로그인이 남지 않아
// 불편하다(2026-11-26 사용자 요청). 수정키를 누른 클릭과 링크 메뉴의 «ao 브라우저에서
// 열기»가 내장 패널로 연다. upstream 은 반대다: 그냥 클릭이 내장 패널, 수정키가 시스템
// 브라우저. 작업공간 파일 미리보기처럼 웹 링크가 아닌 것은 여기를 지나지 않는다.

/**
 * 웹 링크 클릭이 내장 Browser 패널로 열지. false 면 시스템 브라우저로 연다.
 *
 * `hostLocalOnRemoteHost` 는 원격 호스트 세션이 보여 준 localhost 같은 링크다. 이
 * 컴퓨터의 Chrome 이 열면 이 컴퓨터의 localhost 가 열리므로, 그 호스트의 미리보기로
 * 가는 내장 패널 길에 남긴다.
 */
export function ladoWebLinkClickOpensAOBrowser(modifierPressed: boolean, hostLocalOnRemoteHost = false): boolean {
	return modifierPressed || hostLocalOnRemoteHost;
}
